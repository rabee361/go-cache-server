# Plan: Refactor `go-cache-server` from HTTP/JSON to RESP over raw TCP

**Project:** `D:\Go projects\go-cache-server` · **Server package:** `cache_server\` (`go.mod`: module `cache-server`, go 1.25.3)
**Goal:** Replace `net/http` with the standard library `net` package and speak RESP (the Redis wire protocol) instead of HTTP/JSON.

> **Revised 2026-10-05 against the current code state.** Two facts from the first draft are stale and corrected here: (1) the package already compiles and all tests pass — the parser.go stub and the missing `net/http` import were fixed after the draft; (2) `parser.go` no longer exists (deleted) — it will be recreated as the codec file. The draft's Step 0 was written as "get a green baseline"; the baseline is already green, so Step 0 is now purely the removal + interface work, gated on *staying* green.

---

## 0. Verified current state (as of 2026-10-05)

| Fact | Evidence |
|---|---|
| The package **compiles and all tests pass today** | In `cache_server\`: `go build ./...` ✓, `go vet ./...` ✓, `go test ./...` ✓ (`ok cache-server 1.267s`). The two compile errors from the draft are already fixed: `net/http` is now imported in `main.go`, and the `parser.go` stub was deleted |
| `go.mod` lives in `cache_server\`, not the repo root | `go build ./...` at the root fails with "directory prefix . does not contain main module". All `go` commands must run from `cache_server\` |
| The full HTTP/JSON surface is still implemented | `main.go` (225 lines): `Response`/`writeJSON`/`httpStatusText`, 6 `*Handler` methods, `startServer`, `http.HandleFunc` wiring in `main()` |
| Store interface ≠ MemoryStore | Interface: `Delete(key) uint64`, `Exists(key) bool`; Concrete: `Delete(key) (uint64, error)`, `Exists(key) (bool, error)` — documented in `main_test.go` header comment (lines 3–6) |
| Tests: 883 lines, all HTTP/JSON, all passing | Phase 1 store unit tests (keepable), Phase 2 handler tests via `httptest` (rewrite), Phase 3 `httptest.NewServer` e2e (rewrite) |
| `MemoryStore.Delete` always returns `0` | Never reports whether a key was removed — blocks a correct `:n` reply for `DEL` |
| Debug noise | `fmt.Println` in `MemoryStore.Set` (2 lines) and in `getValueHandler` (2 lines) |
| Latent logging bug in `startServer` | `log.Println("Server started ...")` runs *after* `ListenAndServe` returns (lines 88–95) — Step 7 deliberately does not reproduce this |
| Minor `readyHandler` bug | `defer s.Delete(key)` *and* an explicit `s.Delete(key)` — the key is deleted twice; Step 5's port drops the double-delete |
| Consumers of the HTTP API | `demo\demo\go_client.py` (POST/GET/DELETE `/cache/set|get|delete`, JSON envelope; no `/cache/exists` call); `demo\demo\backends.py` wires it as a Django cache backend; `demo\demo\settings.py` also has `django-redis` via `REDIS_URL` (`redis://localhost:6379/0`); `docker-compose.yml` defines a real `redis:7-alpine` on 6379 |
| `README.md` at repo root | Empty (0 bytes); `cache_server\dockerfile` also empty |

---

## 1. Background: what RESP is and why `net` + `bufio`

### 1.1 RESP reply/request types (RESP2 — the only version we need)

RESP (REdis Serialization Protocol) is a tiny text-framed binary-safe protocol. Every value starts with a 1-byte type prefix and ends with `\r\n` (CR LF — **both** bytes, always):

| Type | Wire format | Example bytes | Meaning |
|---|---|---|---|
| Simple string | `+<text>\r\n` | `+OK\r\n` | Success, no payload of interest |
| Error | `-<text>\r\n` | `-ERR unknown command\r\n` | The command failed (stays on same connection) |
| Integer | `:<decimal>\r\n` | `:1\r\n` | Numeric result (counts, lengths) |
| Bulk string | `$<n>\r\n<n bytes>\r\n` | `$5\r\nhello\r\n` | **Binary-safe** payload: exactly `n` raw bytes, then CRLF. Value may contain `\r\n`, NULs, anything. |
| Null bulk string | `$-1\r\n` | `$-1\r\n` | "No value" (e.g. `GET` on a missing key) |
| Array | `*<n>\r\n` + `n` values | `*2\r\n$3\r\nSET\r\n$1\r\nk\r\n` | List of values; `*-1\r\n` = null array (we won't emit it) |

**Client → server requests** are always arrays of bulk strings: `*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n`. **Server → client replies** are one of the reply types above. There is no header, no status code, no JSON envelope — the type prefix *is* the contract.

Also: Redis accepts **inline commands** for humans typing at a terminal — a line like `PING\r\n` (first byte is not `*`) is split on spaces. Our decoder will support both (see Step 3).

### 1.2 Why `net` + `bufio` instead of `net/http`

- `net/http` owns the connection lifecycle, parsing, status lines, headers, and expects a *request/response per HTTP exchange*. RESP is not HTTP: there is no URL, method, or header — a connection is a **long-lived stream of command→reply pairs**. Trying to smuggle RESP through `net/http` means fighting the library (hijacking connections) rather than using it.
- `net.Listen("tcp", addr)` + `Accept()` gives us the raw socket; `net.Conn.Read/Write` gives us the byte stream. That *is* the entire protocol surface RESP needs.
- `net.Conn.Read` is a raw syscall with **no buffering**: reading 1 byte at a time = 1 syscall per byte, and one `Read` may return **half a command** or **three commands at once** (TCP is a stream, not a message queue). `bufio.Reader` batches reads into an internal buffer, and `bufio.Reader.ReadString('\n')` / `io.ReadFull` let us wait for exactly the framing we need. `bufio.Writer` batches writes so a reply of 20 bytes doesn't cost a syscall — **but it only sends when full or when we call `Flush()`**, which is why flushing after every reply is mandatory (a hanging `redis-cli` almost always means a missing `Flush`).

### 1.3 Old HTTP endpoint → new RESP command mapping

| Old HTTP endpoint | Old success payload | New command | New reply |
|---|---|---|---|
| `POST /cache/set` (form `key`,`value`) | `{"code":"SET_OK", ...}` | `SET <key> <value>` | `+OK` |
| `GET /cache/get?key=` | `{"code":"GET_OK","data":{"value":...}}` | `GET <key>` | `$<n>\r\n<value>\r\n`, or `$-1\r\n` if missing |
| `GET /cache/exists?key=` | `{"code":"EXISTS_OK","data":{"exists":true}}` | `EXISTS <key> [<key> ...]` | `:1` / `:0` (count of keys present) |
| `DELETE /cache/delete?key=` | `{"code":"DELETE_OK"}` | `DEL <key> [<key> ...]` | `:1` / `:0` (count of keys removed) |
| `GET /health` (liveness) | `{"code":"HEALTH_OK"}` | `PING` | `+PONG` |
| `GET /health/ready` (store round-trip probe) | `{"code":"READY"}` | `READY` ⚠️ *custom, non-standard* | `+READY` or `-ERR ...` |
| *(none)* | — | `QUIT` | `+OK` then server closes the connection |

Error mapping (replaces JSON error codes):

| Old JSON code | New RESP error |
|---|---|
| `MISSING_KEY` / wrong arity | `-ERR wrong number of arguments for '<cmd>' command` |
| `METHOD_NOT_ALLOWED` | *(disappears — no methods in RESP)* |
| `NOT_FOUND` (GET) | `$-1\r\n` (null, **not** an error — this is correct Redis semantics) |
| `NOT_FOUND` (EXISTS) | `:0` (not an error) |
| unknown command | `-ERR unknown command '<name>'` |
| malformed bytes | `-ERR Protocol error: ...` |

**The entire JSON envelope (`Response`, `writeJSON`, `httpStatusText`, `Content-Type`, HTTP status codes, `Ts` field) is deleted.** Every byte of a reply is now produced by one RESP encoder.

⚠️ **Design decision to record:** keep `READY` (porting `readyHandler`'s set→get→delete self-test) so the refactor doesn't lose the readiness capability, but document it as a non-standard extension. Standard health checks (`PING`) work with any Redis client. If you prefer strict Redis compatibility only, drop `READY` — nothing else depends on it.

---

## 2. Target architecture

```
main()
 └─ store := NewMemoryStore()                      // unchanged semantics, protocol-agnostic
 └─ ln, _ := net.Listen("tcp", addr)               // e.g. "localhost:6379" (or -addr flag)
 └─ for {                                          // ACCEPT LOOP (runs forever)
        conn, err := ln.Accept()
        go handleConn(conn, store)                 // GOROUTINE PER CONNECTION
    }

handleConn(conn, store):
    r := bufio.NewReader(conn)                     // buffered reads: syscall amortized
    w := bufio.NewWriter(conn)                     // buffered writes: MUST Flush
    defer conn.Close()
    for {                                          // one iteration = ONE command
        args, err := readCommand(r)                // DECODE: blocking, CRLF-framed, binary-safe
        if err == io.EOF            -> return      // client hung up: normal close
        if err is protocol error    -> writeReply(w, errorReply(...)); return/continue
        reply := dispatch(store, args)             // DISPATCH: arity check -> handler -> Store
        writeReply(w, reply)                       // ENCODE: RESP bytes into buffer
        w.Flush()                                  // FLUSH: push bytes to the socket NOW
        if reply is QUIT-OK        -> return
    }
```

Key properties:

1. **`MemoryStore` stays exactly as it is** (minus the `Delete` count fix in Step 0): it deals in `string`/`[]byte`, owns its `sync.RWMutex`, and knows nothing about TCP or RESP. Protocol code never reaches into `s.value.data` — it only calls `Get/Set/Delete/Exists`. This is what makes the refactor testable in layers.
2. **Lock discipline:** the store's mutex is taken and released *inside* each store method. Network I/O (`Read`, `Write`, `Flush`) happens **outside any lock**. Holding a lock across a socket write would let one slow client freeze every other client.
3. **Concurrency model:** goroutine-per-connection (cheap: ~8KB stack) + shared mutex-protected store. Each connection owns its private `bufio.Reader`/`Writer`, so no shared mutable buffer state — no cross-connection locking needed above the store.
4. **One read ≠ one command:** the decoder loops until a *complete* command is in hand (read Step 3's why). The connection loop never assumes a `Read` returned a whole command.

### Proposed file layout (minimal, learner-sized)

| File | Responsibility |
|---|---|
| `cache_server/main.go` | `MemoryStore` + `Store` interface (kept), `serve(ln, store)` accept loop, `handleConn`, `main()` wiring |
| `cache_server/parser.go` | **Protocol codec**: `readCommand(*bufio.Reader)` (decoder) + `writeReply(*bufio.Writer, Reply)` (encoder) + the `Reply` type. **New file** — the old stub was deleted; recreate it as the codec's home |
| `cache_server/commands.go` | `dispatch(store, args) Reply` + per-command handlers + arity table |
| `cache_server/main_test.go` | Phase 1 store tests (kept) + new command-level tests |
| `cache_server/e2e_test.go` | Raw-socket end-to-end tests (`net.Dial`, assert exact wire bytes) |

*(Alternative: fold `commands.go` into `main.go`. Two files total is also fine — the boundary that matters is codec vs. command logic.)*

---

## 3. Ordered implementation steps

> Every step ends **green**: `go build ./...` and `go test ./...` (run from `cache_server\` — the repo root has no go.mod) must pass before starting the next one. The suite is green *today*, so each step's job is to change code without regressing it.

---

### Step 0 — Remove HTTP, fix the interface (baseline is already green — keep it that way)

**Why / logic:**
The first draft of this plan opened with two compile breakages ("get a green baseline first"). Both are already fixed: `net/http` is imported in `main.go` and the broken `parser.go` stub has been deleted, so `go build ./...`, `go vet ./...`, and `go test ./...` are all green right now. What remains is the *intended* content of the original Step 0: remove the HTTP surface, fix the interface mismatch, and land one store behavior fix — gated on staying green throughout.

Since we're leaving HTTP entirely, the whole HTTP layer goes away: `Response`, `writeJSON`, `httpStatusText`, the six `*Handler` funcs, `startServer`, the `http.HandleFunc` block in `main()` (leaving a temporary `main()` that starts a TCP listener stub *or* just the store construction — pick the stub so the binary still runs).

Simultaneously fix the `Store` interface mismatch. The `main_test.go` header comment documents it: the interface says `Delete(key) uint64` / `Exists(key) bool`, the concrete type returns two-value versions. Align the **interface to the concrete type** (not vice-versa) so the 300+ lines of Phase 1 store tests keep compiling untouched. Add a compile-time assertion `var _ Store = (*MemoryStore)(nil)` **in the same edit as the fix** — it will not compile until the interface matches, which is exactly its job.

One behavior fix belongs here too: `MemoryStore.Delete` returns `0` unconditionally, so a future `DEL` reply can't distinguish "removed 1 key" from "removed nothing." Make it return `1` if the key existed, `0` otherwise — and update the three `TestDelete` table entries that expect `0` for deletes of existing keys (`delete_existing_key`, `delete_empty_key`, `delete_then_set_again`). Doing it now keeps later steps behavior-only.

Also delete the **Phase 2 + Phase 3 tests** (HTTP handler tests, `TestIntegrationEndToEnd`, `parseResponse`, `parseHTTPResponse`) — they compile against removed symbols. This is intentional and safe: their assertions are the written spec for Step 8, and git history preserves them. Keep Phase 1 store tests (`TestSet`, `TestGet`, `TestDelete`, `TestExists`, `TestConcurrentAccess`) + `newTestStore`/`populateStore`, and drop the now-obsolete header comment about the interface mismatch.

Also in this step: drop the `fmt.Println` debug lines from `Set` and `getValueHandler` (they'd spam the TCP server logs), and drop `readyHandler`'s double-delete (`defer s.Delete(key)` plus the explicit call) so Step 5's port has a clean template.

**Files touched:** `cache_server/main.go` (large deletion + interface fix + `Delete` count + debug-line removal), `cache_server/main_test.go` (delete Phases 2–3, fix 3 `wantRet` values, drop stale header comment).

**Verify:**
```powershell
cd "D:\Go projects\go-cache-server\cache_server"
go build ./...      # stays green
go vet ./...        # clean
go test ./...       # Phase 1 store tests PASS
```

**Checkpoint:** zero references to `net/http`, `httptest`, `encoding/json` anywhere in the package; `var _ Store = (*MemoryStore)(nil)` compiles; only store tests remain and they're green.

---

### Step 1 — Define the command set and the `Reply` type (contract before code)

**Why / logic:**
Get the *types* right and the rest of the code becomes mechanical. Before writing a single byte of I/O, pin down two things:

1. **The command table** (name → min/max args → handler). Fixing arity *now* means the decoder and dispatcher never argue. Keep it deliberately tiny — this is a Redis-*compatible subset*, not Redis:

   | Command | Arity (total args incl. name) | Reply |
   |---|---|---|
   | `PING` | 1 (also allow 2: echoes arg as bulk string, like Redis) | `+PONG` / `$<n>` |
   | `SET key value` | exactly 3 | `+OK` |
   | `GET key` | exactly 2 | bulk string or `$-1` |
   | `DEL key [key ...]` | ≥ 2 | `:<count>` |
   | `EXISTS key [key ...]` | ≥ 2 | `:<count>` |
   | `READY` (custom) | 1 | `+READY` or `-ERR ...` |
   | `QUIT` | 1 | `+OK` then close |

2. **The `Reply` value type** — a small sum type the dispatcher returns and the encoder serializes:

   ```go
   type Reply struct {
       Kind  byte     // '+', '-', ':', '$', '*'
       Str   string   // for '+', '-', '$'
       Int   int64    // for ':'
       Null  bool     // true -> "$-1\r\n"
       Arr   []Reply  // reserved; we emit no arrays in this subset
   }
   ```

   Why a *value* instead of writing bytes directly in each handler? (a) Handlers become pure logic (`dispatch` returns a `Reply`, no I/O) → **unit-testable without a socket**; (b) one encoder owns all byte-formatting → no handler can accidentally emit a malformed reply; (c) tests can assert on `Reply{Kind:':', Int:1}` instead of parsing wire bytes.

   Convenience constructors keep call sites readable: `simpleString("OK")`, `errReply("...")`, `intReply(1)`, `bulkReply(b)`, `nullReply()`.

**Files touched:** `cache_server/parser.go` (new — the `Reply` type + constructors), `cache_server/commands.go` (new — command name/arity table as data).

**Verify:** `go build ./... ; go test ./...`. Quick sanity: a tiny test asserting `bulkReply([]byte("hi")).Kind == '$'`.
**Checkpoint:** types exist, nothing else changed, still green.

---

### Step 2 — The encoder: `writeReply(w *bufio.Writer, r Reply) error`

**Why / logic:**
Write the **output direction first** — it's the easier half, and it lets you hand-test with a fake buffer before any socket exists. The encoder is pure string formatting:

- `+`/`-`/`:` → `fmt.Fprintf(w, "+%s\r\n", ...)` (mind: error messages must not contain `\r`/`\n` — sanitize or strip them).
- `$` → `fmt.Fprintf(w, "$%d\r\n", len(b))`, write the raw bytes, then `w.WriteString("\r\n")`. **Length first, then exactly `len` bytes, then CRLF** — the trailing CRLF is *framing*, not payload; forgetting it desynchronizes the client forever.
- `Null` → `w.WriteString("$-1\r\n")`.

Why write into a `bufio.Writer` rather than the `net.Conn` directly: a reply like `+OK\r\n` is 5 bytes; a direct `conn.Write` is a syscall each time. Buffering batches them.

⚠️ **The encoder does NOT flush.** Flushing is the connection loop's job (Step 6), so that decoding → dispatching → encoding can be tested independently, and so batching remains possible later. Document this on the function.

**Files touched:** `cache_server/parser.go`.

**Verify:** table-driven test writing to a `bytes.Buffer` wrapped in `bufio.Writer`, flushing, and asserting the **exact** byte string:
```text
simpleString("OK")   -> "+OK\r\n"
errReply("ERR x")    -> "-ERR x\r\n"
intReply(42)         -> ":42\r\n"
bulkReply("hello")   -> "$5\r\nhello\r\n"
nullReply()          -> "$-1\r\n"
bulkReply("")        -> "$0\r\n\r\n"     // empty bulk ≠ null bulk!
```
`go test ./...` green. **Checkpoint:** every RESP type round-trips to known bytes.

---

### Step 3 — The decoder: `readCommand(r *bufio.Reader) ([]string, error)`

**Why / logic — the heart of the refactor:**

**Why one `Read` ≠ one command.** TCP is a byte *stream*: the kernel may deliver `*2\r\n$3\r\nGET\r\n$3\r\nfo` in one `Read` and `o\r\n` in the next, or deliver two whole commands in one `Read`. Any code shaped like `n, _ := conn.Read(buf); parse(buf[:n])` is wrong by construction. The fix: **loop, reading until the framing is complete**, using buffered primitives that block until satisfied:

1. Peek the first byte with `r.Peek(1)`.
   - If it's `*` → RESP array form (what real clients send):
     a. Read the line: `r.ReadString('\n')` → parse `*<n>` → `n` must be ≥ 1 and ≤ a sane cap (e.g. 1024 elements).
     b. For each of the `n` elements: read line → must be `$<len>` → `len` must be ≥ 0 and ≤ a **max bulk cap** (e.g. 1 MB; refuse larger with `-ERR Protocol error: invalid bulk length` — this is a DoS guard, a malicious client could otherwise claim `$999999999` and make you allocate a gigabyte).
     c. Read exactly `len` bytes with **`io.ReadFull(r, payload)`** — this is the function that handles partial reads correctly; a naive `r.Read(buf)` would stop early. Then read and verify the trailing `\r\n` (if it isn't CRLF → protocol error).
   - Else → **inline command**: `r.ReadString('\n')`, trim `\r\n`, `strings.Fields(line)` (what a human typing in `nc`/telnet sends: `SET foo bar\r\n`).
2. Return `args []string` with `args[0]` **not yet case-normalized** (Step 4 does `strings.ToUpper`).

**Why CRLF framing matters:** lines are terminated by the two-byte sequence `\r\n`. `ReadString('\n')` gives you the line *including* `\n`; you must trim *both* characters. If you only trim `\n`, every command name ends with a stray `\r` and `GET` silently becomes `GET\r` → "unknown command". For bulk payloads the length is authoritative, so values *may* contain CRLF — that's the "binary-safe" promise: `SET key "line1\r\nline2"` must survive intact.

**Why errors are typed:** distinguish `io.EOF` (client closed → clean loop exit, *not* an error log) from protocol errors (reply `-ERR Protocol error: ...`, then close — Redis closes on framing errors because the stream is unrecoverable) from transient read errors.

**Files touched:** `cache_server/parser.go`.

**Verify:** tests feeding **byte-split input** to prove partial-read handling — build the reader over a custom `io.Reader` that yields 1 byte at a time (or `iotest.OneByteReader`), decode `*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n`, assert `["SET","foo","bar"]`. Also: inline form, empty value (`$0\r\n\r\n`), value containing `\r\n`, oversized length → error, missing trailing CRLF → error, EOF with empty buffer → `io.EOF`. `go test ./...` green. **Checkpoint:** `readCommand` is proven against fragmentation — the #1 TCP bug is now dead.

---

### Step 4 — Dispatcher with arity checks: `dispatch(store Store, args []string) Reply`

**Why / logic:**
The dispatcher is the traffic cop between the wire and the store. Its job, in order:

1. **Empty args** → `-ERR empty command` (possible with a blank inline line).
2. **Case-insensitivity:** `strings.ToUpper(args[0])` — Redis commands are case-*insensitive* (`set`, `Set`, `SET` all work). Real clients send uppercase, humans don't; normalize once, here.
3. **Unknown command** → `-ERR unknown command '<name>'`.
4. **Arity check against the table from Step 1** → `-ERR wrong number of arguments for '<cmd>' command` (Redis's exact wording — error strings are part of the compatibility contract, and tests/clients may match on them).
5. Otherwise call the matching handler.

Why check arity *here* rather than inside each handler: one table, one code path, and handlers can index `args[1]`, `args[2]` without defensive nil/bounds checks — illegal states become unrepresentable (wrong-arity calls never reach a handler).

Note the dispatcher takes the **interface** `Store`, not `*MemoryStore` → tests can pass a fake store that records calls or returns errors, no TCP needed.

**Files touched:** `cache_server/commands.go`.

**Verify:** table-driven tests over `dispatch` alone (no sockets): `["set"]` → error reply containing `wrong number`; `["GET"]` → arity error; `["FOO"]` → `unknown command`; `["get","k"]` on empty store → `Null` reply; `["SeT","k","v"]` (lowercase) → `+OK`. `go test ./...` green. **Checkpoint:** full command semantics provable with zero networking.

---

### Step 5 — Handlers that call the `Store`

**Why / logic:**
Each handler is a thin adapter from "validated args" to "store call" to "Reply" — **no locking code here**. The store's own `sync.RWMutex` already serializes map access; handler-level locking would double-lock (and risk deadlock).

| Handler | Logic | Reply |
|---|---|---|
| `PING` | (optional echo of `args[1]`) | `+PONG` / bulk echo |
| `SET` | `store.Set(args[1], []byte(args[2]))` | `+OK` (on store error: `-ERR set failed: ...`) |
| `GET` | `v, ok := store.Get(args[1])` | `bulkReply(v)` if `ok`, else **`$-1`** — missing key is *not* an error |
| `DEL` | loop keys, count `n` actually removed (needs Step 0's count fix) | `:<n>` |
| `EXISTS` | loop keys, count present | `:<n>` |
| `READY` | port `readyHandler`'s self-test (but **single** delete, not the current double-delete): `Set(tmp)` → `Get` verify → `Delete(tmp)`; any failure → error reply | `+READY` / `-ERR readiness check failed: ...` |
| `QUIT` | none | `+OK` (Step 6 closes the conn) |

**Why no network I/O under the store lock:** the pattern is *decode (I/O) → dispatch (store lock, held only for microseconds inside `Get/Set`) → encode (I/O) → flush (I/O)*. The lock is scoped to map access only. If you instead locked around "read command through write reply," one client on a stalled connection would block all others — the classic throughput-killing mistake.

**Files touched:** `cache_server/commands.go` (handler funcs).

**Verify:** handler-level tests with a fresh `newTestStore()` per case (reuse Phase 1 helpers): set→get round trip, get-missing→Null, DEL counting on present/absent/overlapping keys, EXISTS counting, READY leaves no `_health_check_*` key behind (port the existing `ready_cleanup` assertion). `go test ./...` green. **Checkpoint:** all business behavior provable without a socket.

---

### Step 6 — Connection handler + accept loop (`net.Listen` / `Accept`)

**Why / logic:** finally wire the socket.

```go
func serve(ln net.Listener, store Store) error {          // takes an ln -> testable on :0
    for {
        conn, err := ln.Accept()
        if err != nil { return err }                       // or log+continue for transient errors
        go handleConn(conn, store)                         // WHY GOROUTINE PER CONNECTION:
    }                                                      // a blocking Read on client A's socket
}                                                          // must not stall clients B, C, D...
```

`handleConn` per the architecture diagram, with the non-obvious parts called out:

- **Why goroutine-per-connection:** `readCommand` blocks in `Read` until the client sends something. In a single-threaded loop, one idle/slow client stops the whole server. Go goroutines are cheap (~KBs), so the idiomatic answer is one per connection — this is exactly what `net/http` does internally; now we're doing it ourselves.
- **Why `bufio` on both directions:** reads batch syscalls; writes batch syscalls. Reads also give us the line/`ReadFull` semantics Step 3 depends on.
- **Why `Flush()` after every reply:** `bufio.Writer` holds bytes in memory until full or flushed. Without a flush, `redis-cli` sends `PING` and waits forever while `+PONG` sits in our buffer. One flush per command is the correct, simple cadence.
- **EOF handling:** `readCommand` returning `io.EOF` on a *fresh* command = client disconnected (e.g. `QUIT` peer close, Ctrl-C in `nc`) → `return` quietly, `defer conn.Close()`. An EOF *mid-command* = truncated stream → treat as protocol error/close. Log unexpected errors; never panic — one bad client must not kill the server.
- **Error policy:** protocol errors → write `-ERR Protocol error: ...`, flush, close (stream unusable). Command errors (`unknown command`, arity) → write `-ERR ...`, flush, **keep the connection open** (Redis behavior — an error is not fatal).
- **`QUIT`:** after flushing `+OK`, `return` → `defer` closes the conn.
- Keep a `maxBulk`/`maxArgs` cap visible here (enforced in Step 3).

**Files touched:** `cache_server/main.go`.

**Verify:**
```powershell
go build ./... ; go vet ./...
# manual smoke test (server running):
redis-cli -h 127.0.0.1 -p 6379 PING          # PONG
redis-cli -p 6379 SET foo bar                 # OK
redis-cli -p 6379 GET foo                     # "bar"
redis-cli -p 6379 DEL foo                     # (integer) 1
redis-cli -p 6379 GET foo                     # (nil)
# no redis-cli? raw PowerShell socket:
$c=New-Object Net.Sockets.TcpClient('127.0.0.1',6379);$s=$c.GetStream()
$b=[Text.Encoding]::ASCII.GetBytes("PING`r`n");$s.Write($b,0,$b.Length)
$s.Read($b,0,64)   # expect "+PONG\r\n"
```
**Checkpoint:** a real Redis client interoperates end-to-end. (If `redis-cli` isn't installed, the Step 8 e2e tests are the substitute — you may therefore do Steps 6→8 back-to-back.)

---

### Step 7 — `main()` wiring + port choice

**Why / logic:**
```go
func main() {
    addr := flag.String("addr", "localhost:6379", "listen address")  // env override optional
    ln, err := net.Listen("tcp", *addr)
    ...
    log.Printf("RESP cache server listening on %s", *addr)
    log.Fatal(serve(ln, NewMemoryStore()))
}
```
Port rationale: **6379** is *the* Redis port, so `redis-cli` (no flags), health checks, and any tooling default to it with zero config. Trade-off: the old server used `localhost:8080`, and `docker-compose.yml` already maps a real `redis:7-alpine` to host 6379 — if you run both, they collide. Mitigations: make the addr a flag (`-addr localhost:6380`) and document the conflict in the README.

Also: log the address *before* `serve` blocks. The current `startServer` has the opposite bug — it logs "Server started on localhost:8080" only *after* `ListenAndServe` returns (i.e., after the server has stopped) — worth not reproducing. Flag parse failure / listen failure must exit non-zero with a clear message (bind errors like "address already in use" are the first thing a learner will hit).

**Files touched:** `cache_server/main.go`.

**Verify:**
```powershell
go build ./...
.\cache-server.exe -addr localhost:6380     # start
redis-cli -p 6380 PING                      # PONG
# second instance on same port -> clear "address already in use" error
```
**Checkpoint:** configurable, documented bind; clean startup/shutdown logging.

---

### Step 8 — Rewrite the tests (command-level + raw-socket e2e)

**Why / logic:**
The old suite tested HTTP semantics that no longer exist (status codes, `Content-Type`, JSON `code` fields). Replace with two layers mirroring the architecture:

**Layer 1 — command-level (no sockets), in `main_test.go`:** the Phase 1 store tests are already kept from Step 0; Steps 4–5 added `dispatch`/handler tests. Consolidate into a table: *input args + store state → expected `Reply`*. Port every meaningful old assertion to its RESP equivalent using this translation guide (this is why deleting the old tests in Step 0 was safe):

| Old assertion | New assertion |
|---|---|
| `code == "SET_OK"`, status 200 | `Reply{'+',"OK"}` |
| `code == "NOT_FOUND"` (GET), 404 | `Reply{Null:true}` (`$-1`) |
| `code == "EXISTS_OK"` + `data.exists` | `Reply{':', 1}` |
| `code == "DELETE_OK"` | `Reply{':', 1 or 0}` |
| `code == "MISSING_KEY"`, 400 | `-ERR wrong number of arguments...` |
| `code == "METHOD_NOT_ALLOWED"`, 405 | *(no equivalent — gone)* |
| `code == "HEALTH_OK"` / `"READY"` | `+PONG` / `+READY` |
| store state verified via `store.Get` | unchanged — keep these, they're protocol-agnostic |

**Layer 2 — e2e over a real socket, new `e2e_test.go`:** start the server **on an ephemeral port** (`net.Listen("tcp", "127.0.0.1:0")` + `go serve(ln, store)`) — this is *why* `serve` takes an `ln` parameter (Step 6) instead of creating its own: no port collisions, no sleeps, parallel-safe. Then:
```go
conn, _ := net.Dial("tcp", ln.Addr().String())
fmt.Fprintf(conn, "*1\r\n$4\r\nPING\r\n")
got, _ := bufio.NewReader(conn).ReadString('\n')
// assert got == "+PONG\r\n"   <-- assert EXACT wire bytes
```
Cover: PING, SET→GET round-trip (including a value containing `\r\n` and binary bytes), GET missing → `$-1\r\n`, EXISTS/DEL counts, unknown command, wrong arity, inline-command form, **two commands pipelined in one `Write`** (proves the decoder handles multiple commands per TCP segment), `QUIT` → `+OK` then EOF, and a **concurrent** test (N goroutines dialing and doing SET/GET) to replace `TestConcurrentAccess`'s spirit.

Why assert exact bytes rather than a parsed structure: the wire format *is* the product — a parser-side bug would be invisible if both sides used the same parser.

**Files touched:** `cache_server/main_test.go` (finish/consolidate), `cache_server/e2e_test.go` (new).

**Verify:**
```powershell
go test ./...          # everything green
go test -race ./...    # REQUIRED: proves store locking holds under concurrent conns
go vet ./...
```
**Checkpoint:** `go test -race` clean, e2e asserts byte-exact replies, all old behaviors from the translation table are covered.

---

### Step 9 — README / docs + declare consumer migration out of scope

**Why / logic:**
The repo README is empty and the protocol is now non-obvious to the next reader. Write (in `README.md` at repo root):

1. What the server is: in-memory cache speaking a Redis-compatible RESP subset.
2. Supported commands table (from Step 1) + explicit "not implemented" list (no persistence, no expiry, no pub/sub, no RESP3, no MULTI, single DB).
3. Run + connect examples (`go run .`, `redis-cli -p 6379`), the `-addr` flag, the 6379/compose port caveat.
4. **Breaking-change notice:** the HTTP/JSON API is removed; the wire protocol and default port changed.
5. Project layout (which file owns what) — this doubles as the refactor's map.

Update `.dockerignore`/compose/`dockerfile` notes only if they reference the HTTP server (the cache_server `dockerfile` is currently empty — either fill in a minimal multi-stage build or flag it as a known gap; do *not* silently expand scope).

**Explicitly document as follow-up / out-of-scope:** `demo\demo\go_client.py` (`requests.post(f"{host}/cache/set")` etc.) and therefore `demo\demo\backends.py`'s Django cache backend **will break** — they speak HTTP/JSON to a server that now speaks RESP. Migration options to record (not do): use `redis-py` against the RESP server (it *is* wire-compatible for our subset), or write a tiny socket client, or keep an HTTP façade. Note `demo` also has `django-redis` configured via `REDIS_URL` (`redis://localhost:6379/0`), so the Django app has a second, unaffected path.

**Files touched:** `README.md` (root), possibly `cache_server/dockerfile`.

**Verify:** README examples actually work by copy-paste; `go build ./... && go test ./...` still green. **Checkpoint:** a newcomer can start the server and issue each command from the README.

---

## 4. Risks & pitfalls

| # | Risk | Why it bites | Mitigation |
|---|---|---|---|
| 1 | **Partial reads / TCP fragmentation** | `conn.Read` returns *some* bytes, not a command; a naive single-Read parser works in tests and fails in production | Decoder loops with `ReadString`/`io.ReadFull`; prove with 1-byte-at-a-time reader tests (Step 3) |
| 2 | **Multiple commands per segment** | Pipelined clients (`*1\r\nPING\r\n*1\r\nPING\r\n` in one write) — parser that reads "one command per Read" desyncs | Outer loop keeps decoding until buffer empty; add pipelined e2e test |
| 3 | **Forgotten `Flush()`** | Reply sits in `bufio.Writer`; client hangs with no error — the most common beginner symptom | Flush after every reply in `handleConn`; e2e test would catch it instantly |
| 4 | **Trailing-CRLF / length mistakes** | Off-by-one in `$n\r\n...\r\n` desyncs the stream permanently | Byte-exact encoder tests (Step 2) + `io.ReadFull` (Step 3) |
| 5 | **EOF vs. error conflation** | Logging every disconnect as an error (noise) or treating mid-command EOF as clean exit (hides truncation) | Distinguish `io.EOF` at command boundary (clean) vs. mid-command (protocol error) |
| 6 | **Command case / stray `\r`** | `get` must work; untrimmed `\r` yields "unknown command 'GET\r'" | `strings.ToUpper(args[0])` + trim *both* CR and LF; test lowercase input |
| 7 | **Unknown command / arity** | Indexing `args[2]` without checking = panic = whole server dies (panics in a conn goroutine kill the process) | Arity table checked in `dispatch` *before* handlers; consider `defer recover()` in `handleConn` as a belt-and-braces guard |
| 8 | **Lock held during I/O** | One stalled client blocks all others; possible deadlock if a handler ever writes while holding `mu` | Lock only inside store methods; encode/flush outside; `go test -race` + concurrency e2e |
| 9 | **Huge allocation via bulk length** | Client sends `$999999999999` → OOM | Max bulk cap (e.g. 1 MB) → `-ERR Protocol error: invalid bulk length` |
| 10 | **GET-missing semantics** | Returning `-ERR not found` breaks every Redis client; must be `$-1` | Encoder `Null` flag; wire-byte assertion in e2e |
| 11 | **Large test rewrite (scope risk)** | ~590 of 883 test lines die; risk of silently dropping coverage | Translation table (Step 8) maps every old assertion → new one; do it in the same PR as the code, not later |
| 12 | **Regressing a green baseline** | The suite is green *today* — new code can silently break `go build`/`go test`, and errors get misattributed to the refactor | Verify at the start and end of every step; each step's "Verify" block is the gate |
| 13 | **`MemoryStore.Delete` count** | Always `0` → `DEL` always replies `:0` | Fixed in Step 0 (+3 test expectations) |
| 14 | **Consumers break** | `go_client.py` HTTP/JSON and Django backend hit removed endpoints; `docker-compose` maps real Redis to 6379 (port clash with new default) | README breaking-change notice; `-addr` flag; migration logged as out-of-scope follow-up |
| 15 | **Error strings are contract** | Clients/tests match exact Redis wording | Use Redis's literal messages (`wrong number of arguments for '%s' command`, `unknown command '%s'`) |
| 16 | **Binary-unsafe values** | Line-splitting a value corrupts data containing `\r\n`/NUL | Bulk strings are length-delimited; e2e test with CRLF + non-UTF8 bytes |
| 17 | **Goroutine leak on abandon** | Client opens conn and vanishes without closing | Optional: `conn.SetReadDeadline` idle timeout; at minimum, EOF cleanup via `defer conn.Close()` |

---

## 5. Checkpoints (gate before the next step)

| Gate | Must be true |
|---|---|
| **After 0** | `go build ./...`, `go vet ./...`, `go test ./...` **remain green**; zero `net/http`/`httptest`/`encoding/json` references; `var _ Store = (*MemoryStore)(nil)` compiles (interface aligned to the concrete type); Phase 1 store tests green; `Delete` returns real counts |
| **After 1** | Command/arity table + `Reply` type exist; still green |
| **After 2** | Encoder unit tests assert exact bytes for all 6 reply shapes (`+ - : $ null $0-empty`); green |
| **After 3** | Decoder tests pass with 1-byte-at-a-time input, inline form, CRLF-in-value, malformed input → typed error; green |
| **After 4** | `dispatch` table tests: unknown command, wrong arity, case-insensitivity, all covered; green |
| **After 5** | Handler tests cover every command incl. READY cleanup; green |
| **After 6** | `redis-cli` (or raw socket) interoperates: PING/SET/GET/DEL/EXISTS/QUIT; server survives a killed client mid-command |
| **After 7** | `-addr` flag works; bind conflict produces a clear error; startup log prints before blocking (the old `startServer` bug not reproduced) |
| **After 8** | **`go test -race ./...` green**; e2e asserts byte-exact replies; pipelined-commands test passes; translation table shows 1:1 coverage of old assertions |
| **After 9** | README examples verified by copy-paste; breaking changes and the `demo/` follow-up documented |

**Final acceptance:** `go build ./... && go vet ./... && go test -race ./...` clean, plus a live `redis-cli` session performing the full PING/SET/GET/EXISTS/DEL/QUIT cycle against the running binary.

---

## 6. Out of scope / follow-ups (recorded, not planned)

- **Migrating `demo/demo/go_client.py` + `backends.py`** to a RESP client (`redis-py` is wire-compatible with this subset) — breaking change documented in README.
- **Full Redis**: persistence, TTL/EXPIRE, `SELECT`/DBs, `MGET/MSET`, pipelining optimizations, RESP3, AUTH, pub/sub, clustering.
- **Empty `cache_server/dockerfile`** and the compose port-6379 collision with `redis:7-alpine` — flag in README; fix separately if desired.
- **Idle-connection read deadlines / graceful shutdown (SIGINT)** — nice-to-have hardening after the core lands.
