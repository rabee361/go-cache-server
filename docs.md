# Architecture decisions — go-cache-server

This document walks through the reasoning behind each architectural decision in the HTTP/JSON → RESP refactor, and later the minimal-scope trim. Each section states the decision, the chain of logic that led to it, and the alternative that was rejected. Sections describing features that no longer exist are annotated as removed with a pointer to §24, the trim rationale.

## 1. Speak RESP instead of HTTP/JSON

**Decision:** replace the HTTP/JSON endpoints with RESP (the Redis wire protocol) over raw TCP.

**Logic steps:**

1. The server's job is a key-value cache. Its API surface is tiny: set, get, delete, exists, health.
2. For this shape of API, HTTP adds a large layer of machinery (methods, headers, status codes, content types, URL routing) that carries no information — every old endpoint was effectively `{code, data}`.
3. Redis is the de-facto standard for in-memory caches. Speaking RESP makes the server **interoperable with the entire Redis ecosystem** (`redis-cli`, `redis-py`, Django's `django-redis`, monitoring tools) with zero client-side work.
4. RESP itself is deliberately simple: one type byte + CRLF framing. Implementing a subset is a ~100-line codec, far smaller than the HTTP stack it replaces.

**Rejected alternative:** keeping HTTP/JSON. It would keep the `demo/` Python client working, but leaves a custom protocol with no tooling and no ecosystem — every consumer must be hand-written.

## 2. `net` + `net` instead of `net/http`

**Decision:** use `net.Listen`/`Accept`/`net.Conn` directly, not `net/http`.

**Logic steps:**

1. HTTP is a request/response-per-exchange protocol; RESP is a **long-lived stream of command→reply pairs** on one connection.
2. `net/http` owns connection lifecycle, request parsing, and routing. Smuggling RESP through it requires connection hijacking — fighting the library instead of using it.
3. RESP needs exactly what the `net` package provides: a listener, accepted sockets, and raw byte streams. There is no HTTP-shaped problem left to solve.

**Rejected alternative:** `http.Hijacker` on top of `net/http`. Workable but adds an entire HTTP handshake for no benefit, and the hijack path is the least-tested corner of `net/http`.

## 3. `bufio.Reader` / `bufio.Writer` on both directions

**Decision:** wrap every connection in a `bufio.Reader` for reads and a `bufio.Writer` for writes.

**Logic steps:**

1. `net.Conn.Read/Write` map (nearly) 1:1 to syscalls. A `+OK\r\n` reply is 5 bytes; the overhead of a syscall per reply dominates.
2. `bufio.Reader` batches inbound bytes into an internal buffer, and provides the primitives the protocol needs: `ReadString('\n')` for CRLF-delimited lines and `io.ReadFull` for length-delimited payloads (see §5).
3. `bufio.Writer` batches outbound bytes. It only sends when its buffer fills or when `Flush()` is called — which is why §4 exists.

**Rejected alternative:** a hand-rolled buffer, or reading one byte at a time with `conn.Read`. Reinventing buffering is error-prone; unbuffered byte-at-a-time reads cost a syscall per byte.

## 4. Flush after every reply

**Decision:** `handleConn` calls `w.Flush()` after every reply, and the encoder itself never flushes.

**Logic steps:**

1. `bufio.Writer` holds bytes in memory until full or flushed — an unflushed `+PONG` sits in the buffer forever and the client hangs with no error.
2. Putting the flush in the connection loop (not in `writeReply`) keeps the encoder a pure, testable formatting function and leaves the door open for batching multiple replies later.
3. One flush per command is the correct simple cadence; the buffer still amortizes the *parts* of a single reply.

**Rejected alternative:** flushing inside the encoder. It couples encoding to I/O timing and makes unit tests depend on a real writer.

## 5. The decoder loops until framing is complete (`ReadString` + `io.ReadFull`)

**Decision:** `readCommand` never assumes one `Read` delivers one command; it blocks on buffered primitives until a full command is in hand.

**Logic steps:**

1. TCP is a byte **stream**, not a message queue. The kernel may deliver half a command, or three commands, in one `Read`.
2. Code shaped like `conn.Read(buf); parse(buf[:n])` is therefore wrong by construction — it works in a demo and desynchronizes in production.
3. The fix: loop with primitives that block until satisfied — `ReadString('\n')` for lines, and `io.ReadFull` for `$<n>` payloads. `io.ReadFull` is the one that correctly handles partial reads; a naive `r.Read` stops at whatever bytes happened to arrive.
4. Bulk payloads are **length-delimited**, so values may contain `\r\n` and NUL bytes — `SET k "line1\r\nline2"` survives intact because the length field, not a line boundary, says where the value ends.

**Verification in tests:** the decoder is fed through `iotest.OneByteReader`, proving it handles input fragmented one byte at a time, plus a pipelined e2e test sending two commands in a single `Write`.

## 6. Supporting inline commands

**Decision:** accept both RESP array framing (first byte `*`) and inline commands (a plain `SET foo bar` line).

**Logic steps:**

1. Real clients send arrays; humans at a terminal (`nc`, telnet, `redis-cli -h`) send lines.
2. Redis itself accepts both, and the inline form costs ~4 lines: read the line, trim CRLF, split on whitespace.
3. It makes the server debuggable by hand — the single most useful property during development.

**Rejected alternative:** arrays only. Slightly simpler decoder, but every manual smoke test requires constructing `*n\r\n$m\r\n` by hand.

## 7. Typed errors: `io.EOF` vs. protocol errors

**Decision:** the decoder returns `io.EOF` only for a clean close before any bytes of a new command; everything else (truncation, bad length, missing terminator) is a protocol error.

**Logic steps:**

1. A client disconnecting is a normal, frequent event — it must close the connection quietly, not spam logs.
2. A framing violation is different: the byte stream is unrecoverable. Redis's policy (and ours): reply `-ERR Protocol error: ...`, flush, close.
3. Distinguishing them by error type (`errors.Is(err, io.EOF)`) rather than string matching keeps the connection loop's control flow honest. `io.ReadFull` already distinguishes for us — it returns `io.ErrUnexpectedEOF` (not `io.EOF`) for mid-payload truncation.

## 8. A `Reply` value type and one encoder

**Decision:** handlers return a small `Reply` struct (`parser.go`); a single `writeReply` function owns all byte formatting.

**Logic steps:**

1. If each handler wrote bytes directly to the socket, handlers would need a socket (untestable without one) and could each introduce a formatting bug.
2. Returning a value makes handlers **pure functions** of `(store, args)` — fully unit-testable, no I/O.
3. One encoder means the wire format has one implementation; a malformed reply can only come from one place.
4. Tests assert on values (`intReply(2)`, `nullReply()`) instead of parsing wire bytes.

**Rejected alternative:** handlers writing to a shared writer. It trades away testability and centralizes nothing.

## 9. Central dispatcher with an arity table

**Decision:** one `dispatch` function checks arity against a data table before any handler runs.

**Logic steps:**

1. Every command has an arity contract (`SET` = 3, `GET` = 2, `DEL` = ≥2, ...). Encoding it as data (`commandTable`) makes the contract visible in one place.
2. Checking arity centrally means handlers can index `args[1]`, `args[2]` **without bounds checks** — wrong-arity input can never reach a handler, so illegal states are unrepresentable.
3. The error wording matters: Redis clients and tests match on `ERR wrong number of arguments for '<cmd>' command`, so we use Redis's literal messages.

**Rejected alternative:** arity checks inside each handler. Duplicated logic, and a missed check becomes an index-out-of-range panic — which kills the whole process, not just the connection.

## 10. Case-insensitive command names

**Decision:** `dispatch` normalizes `args[0]` with `strings.ToUpper` before the table lookup.

**Logic steps:**

1. Redis commands are case-insensitive; real clients send uppercase, humans don't.
2. Normalizing once at the dispatch boundary is cheaper than making the table store every case variant.
3. Note this pairs with §5's CRLF trimming: a stray `\r` on the command name would otherwise turn `GET` into `GET\r` → "unknown command". The decoder trims **both** CR and LF.

## 11. The store stays protocol-agnostic

**Decision:** `MemoryStore` deals only in `string`/`[]byte`; it knows nothing about TCP or RESP, and its mutex is taken and released entirely inside its own methods.

**Logic steps:**

1. Separating storage from protocol makes each layer testable independently (the refactor's whole testing strategy depends on this).
2. Locks scoped inside store methods guarantee **no lock is ever held across network I/O**. If a handler held the store lock while writing to a stalled socket, one slow client would freeze every other client — the classic throughput killer. With locks confined to map access, the critical section is microseconds.

**Rejected alternative:** handlers taking `s.mu.Lock()` directly and doing I/O under it. Deadlock-prone and destroys concurrency.

## 12. Handlers depend on the `Store` interface, not `*MemoryStore`

**Decision:** `dispatch` and every handler take `Store` (the interface), which `MemoryStore` satisfies via a compile-time assertion `var _ Store = (*MemoryStore)(nil)`.

**Logic steps:**

1. Command logic needs only four operations (`Get/Set/Delete/Exists`) — that is the interface, and it was aligned to the concrete signatures (`Delete(key) (uint64, error)`, `Exists(key) (bool, error)`) so the existing store tests keep compiling untouched.
2. Tests can substitute a fake `Store` to record calls or inject errors — no TCP, no real map.
3. The compile-time assertion makes any future drift between interface and implementation a build error instead of a runtime surprise.

## 13. Goroutine per connection

**Decision:** the accept loop spawns `go handleConn(conn, store)` for every connection.

**Logic steps:**

1. `readCommand` blocks until the client sends something. In a single-threaded loop, one idle or slow client stalls the entire server.
2. Goroutines are cheap (a few KB of stack), and each connection owns private `bufio.Reader`/`Writer` — no shared mutable buffer state, so no cross-connection locking is needed above the store.
3. This is the same model `net/http` uses internally; we're just doing it explicitly now.

**Rejected alternative:** an event loop / manual connection multiplexing. Considerably more code for no benefit at this scale.

## 14. `serve` takes a `net.Listener` instead of creating one

**Decision:** `serve(ln net.Listener, store Store)` accepts a listener from its caller.

**Logic steps:**

1. `main` passes a listener bound to the configured address; tests pass one bound to `127.0.0.1:0` (an OS-assigned ephemeral port).
2. Ephemeral ports eliminate port collisions and `time.Sleep`-based "wait for startup" hacks — tests start servers in parallel safely.
3. It also separates "choose the address" (a configuration concern) from "run the accept loop" (a runtime concern).

## 15. `QUIT` is handled in `handleConn`, not in the handler

> **Removed in the minimal-scope trim (§24)** — `QUIT` is no longer a command; clients simply close the connection, and `handleConn` returns on `io.EOF`.

**Decision:** `cmdQuit` just returns `+OK`; the connection loop closes the connection when it sees a successful `QUIT` reply.

**Logic steps:**

1. Closing the connection is a **connection lifecycle** operation, not a command semantic — the handler's job ends at producing the reply.
2. Checking `reply.Kind == '+' && reply.Str == "OK" && args[0] == "QUIT"` (rather than closing on any `QUIT`) matches Redis: a wrong-arity `QUIT` gets an error and the connection stays open.

## 16. Missing `GET` replies `$-1`, not an error

**Decision:** a missing key is a null bulk string, not `-ERR`.

**Logic steps:**

1. In RESP, `$-1` is the defined "no value" sentinel. Redis clients encode their nil handling around it.
2. Returning `-ERR not found` for a miss would make every Redis client treat a routine cache miss as a failure — breaking the client contract (error replies also abort pipelined sequences in many clients).
3. The encoder has an explicit `Null` flag that emits exactly `$-1\r\n`; the empty-string case remains distinct (`$0\r\n\r\n`), and both are pinned by byte-exact tests.

## 17. Bulk-length caps (DoS guard)

**Decision:** the decoder refuses bulk strings over 1 MB and commands over 1024 arguments with a protocol error.

**Logic steps:**

1. The decoder must allocate `n+2` bytes for a bulk payload, where `n` comes from the **attacker-controlled** length field.
2. A client claiming `$999999999` would make the server allocate a gigabyte — or die trying.
3. A cap turns that into a cheap error reply and connection close. 1 MB is far beyond any realistic cache value here; the test suite already exercises 1 MB values as a boundary.

## 18. `READY` as a non-standard command (removed)

> **Removed in the minimal-scope trim (§24).** The decision below was valid for the earlier, broader build; the trimmed server has no `PING` to fall back on either, and health checks are out of scope until commands are grown back.

**Decision (historical):** port the old `/health/ready` self-test to a `READY` command instead of dropping it.

**Logic steps:**

1. Readiness (prove the store can set→get→delete, not just that the socket is open) is a real capability worth preserving.
2. But it is **not** a Redis command, so it's documented as an extension in the README, and standard health checks use `PING`.
3. The port also fixed the old handler's double-delete (`defer s.Delete` + explicit call) — the new `READY` deletes the temp key exactly once on every path, verified by a test that scans for orphaned `_health_check_*` keys.

## 19. `MemoryStore.Delete` returns a count

**Decision:** `Delete` returns `1` if the key existed, `0` otherwise.

**Logic steps:**

1. RESP `DEL` must reply with **the number of keys actually removed** (`:n`).
2. The old implementation returned `0` unconditionally, making that reply impossible to produce correctly.
3. One store-level fix (plus three test expectation updates) enables the whole `DEL` contract; without it the handler would need a separate `Exists` probe per key — racy and redundant.

## 20. Port 6379 by default, with an `-addr` flag

**Decision:** default listen address `localhost:6379`, overridable via `-addr`.

**Logic steps:**

1. 6379 is *the* Redis port — `redis-cli`, health checks, and tooling default to it with zero configuration, which maximizes the interop payoff of §1.
2. The trade-off is a collision with the repo's `docker-compose.yml`, which maps a real `redis:7-alpine` to host 6379. The flag (`-addr localhost:6380`) and a README caveat cover it.
3. Startup logs the address **before** the accept loop blocks — the old `startServer` logged "Server started" only after `ListenAndServe` returned (i.e., after the server had stopped), a bug not worth reproducing. Bind failures exit non-zero with a clear message, since "address already in use" is the first error anyone hits.

## 21. Byte-exact assertions in the e2e tests

**Decision:** e2e tests dial the server and assert the **exact wire bytes** of replies (`"+PONG\r\n"`, `"$-1\r\n"`), not a parsed structure.

**Logic steps:**

1. The wire format *is* the product — a client can only see bytes.
2. If both test and implementation used the same parser, a parser-side bug (wrong length prefix, missing trailing CRLF, `$-1` vs `$0`) would be invisible. Byte comparisons pin the format itself.
3. The suite covers the fragmentation and binary-safety cases on the wire too: pipelined commands in one `Write`, a value containing `\r\n\x00\xff`, `QUIT` → `+OK` then EOF.

## 22. Three test layers mirroring the architecture

**Decision:** store unit tests (kept from the old suite) → codec/dispatch/handler tests (no sockets) → raw-socket e2e (ephemeral port).

**Logic steps:**

1. Each layer tests one seam: the store's locking and semantics, the pure command logic, and the assembled TCP server.
2. A translation table (`SET_OK` → `+OK`, `NOT_FOUND` → `$-1`, `MISSING_KEY` → arity error, ...) mapped every old HTTP assertion to its RESP equivalent so no behavior coverage was silently dropped.
3. Everything below the e2e layer runs with zero networking, so failures are attributable to exactly one layer.

## 23. `recover()` guard in `handleConn`

**Decision:** each connection goroutine has a deferred `recover()` that logs and exits.

**Logic steps:**

1. A panic in any goroutine terminates the **entire process** — one malformed command could kill the server for everyone.
2. The arity table (§9) already makes handler panics unrepresentable, but the guard is cheap insurance at the one boundary that touches attacker-controlled input.
3. Scoping it per-connection means a hypothetical panic costs one client, not the fleet.

## 24. Minimal scope: four commands for Django queryset caching

**Decision:** the server implements exactly `SET (EX/PX)`, `GET`, `DEL`, and `CLIENT SETINFO`; everything else (PING, SETEX/PSETEX, NX/XX, MGET/MSET, INCR family, EXPIRE/TTL, FLUSHDB, SCAN, SELECT, EXISTS, QUIT, READY, multiple logical DBs, the glob matcher) was removed.

**Logic steps:**

1. The server exists to back one Django demo view that calls `cache.get`/`cache.set` through Django's built-in `RedisCache` (redis-py). On `redis://localhost:6379/0` that client sends only: `CLIENT SETINFO` (twice per connection, redis-py ≥ 5), `GET`, and `SET key value EX 300`. Every other command was speculative surface area.
2. `DEL` stays so a bad cached value can be cleared manually during testing — it is variadic, so Django's `delete`/`delete_many` keep working too.
3. Shrinking the `Store` interface to three methods (`Get`, `Set`, `Delete`) collapsed the session/DB indirection into a single map — the session abstraction existed only to carry `SELECT` state, and with `SELECT` gone the store **is** the session.
4. Redis error strings are preserved verbatim (`ERR syntax error`, `ERR invalid expire time in 'set' command`, `ERR unknown command`, arity wording, CLIENT subcommand wording) so real clients report the failures they expect, and tests pin them.
5. Protocol errors close the connection **silently** — no `-ERR Protocol error:` reply is written. Command errors still get replies; only malformed framing kills the connection (this was a deliberate hand edit, kept as-is).
6. Cost of the trim: Django features beyond get/set (`get_many`, `set_many`, `incr`, `touch`, `clear`) now error with "unknown command", non-zero DB URLs fail on `SELECT`, and there is no `PING` for health checks.

## 25. `HELLO` negotiates RESP3 per connection

**Decision:** add `HELLO [2|3]`; the negotiated protocol is per-connection state, not global, and the reply encoder consults it on every write.

**Logic steps:**

1. RESP3 is a superset where the wire form of a reply depends on the negotiated version: a missing `GET` is `$-1\r\n` in RESP2 but `_\r\n` in RESP3, and `HELLO`'s server info is a flat array in RESP2 but a map (`%7`) in RESP3. One `Reply` value must therefore be *version-agnostic*: null stays a null bulk (`Kind: '$'` with `Null`), and the server info is stored as alternating key/value pairs (`Kind: '%'`). The encoder decides the wire form from the `proto` argument.
2. The protocol version is **connection state** (redis-py sends `HELLO 3` once after connect), so it lives in a `clientState` alongside a unique connection id. `cmdHello` mutates it; `handleConn` reads `st.proto` *after* dispatch, so the switch takes effect on the `HELLO` reply itself — exactly how Redis behaves.
3. The connection id comes from a package-level `atomic` counter, so the map's `id` field is stable and unique per connection without a connection registry.
4. `HELLO 4` (or junk) replies Redis's exact `NOPROTO unsupported protocol version` and leaves the protocol unchanged — errors must not have side effects.
5. Options are rejected honestly: `AUTH` returns Redis's no-password error (there is no auth), `SETNAME` errors as unsupported (there is no client-name registry). Silently accepting either would be lying to the client.
6. The server-info reply mirrors Redis's seven fields (`server`, `version`, `proto`, `id`, `mode`, `role`, `modules`) with honest values, and `modules` is the empty array — redis-py parses all of them.

## Deferred / out of scope

- **Growing the command set back** — string commands, data types, the expiry engine, persistence, replication, the rest of RESP3 (sets, doubles, booleans, verbatim strings, push messages) are all deferred.
- **Hardening**: idle-connection read deadlines, graceful shutdown on SIGINT, filling in the empty `cache_server/dockerfile`.
