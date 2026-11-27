# go-cache-server

An in-memory cache server that speaks a Redis-compatible subset of **RESP** (the Redis serialization protocol) over raw TCP. No HTTP, no JSON — any RESP client (e.g. `redis-cli`, `redis-py`) can talk to it.

The server is intentionally minimal: it implements exactly what a Django project needs for basic queryset caching via `django.core.cache.backends.redis.RedisCache` (`cache.get` / `cache.set`), plus `DEL` to clear a bad cached value, `HELLO` for optional RESP3, the basic list/set/sorted-set tier Celery's redis transport needs (`LPUSH`/`BRPOP`/`LREM`/`SADD`/`ZADD`/`ZRANGEBYSCORE`), and `CONFIG GET/SET` for runtime tuning of `maxmemory`/`maxclients`. Everything else was removed on purpose and can be grown back on demand.

## Supported commands

| Command | Syntax | Reply |
|---|---|---|
| `SET` | `SET key value [EX sec\|PX ms]` | `+OK` |
| `GET` | `GET key` | bulk string, or null (`$-1` in RESP2, `_` in RESP3) if missing/expired |
| `DEL` | `DEL key [key ...]` | `:<count>` — number of keys actually removed |
| `EXISTS` | `EXISTS key [key ...]` | `:<count>` — expired keys count as absent |
| `FLUSHDB` | `FLUSHDB` | `+OK` — clears every key in the store |
| `HELLO` | `HELLO [2\|3]` | server info (flat array in RESP2, map in RESP3); `HELLO 3` switches the connection to RESP3 |
| `CLIENT` | `CLIENT SETINFO lib-name lib-ver` | `+OK` — accepts the redis-py handshake |
| `PING` | `PING [message]` | `+PONG`, or the message echoed back as a bulk string |
| `CONFIG` | `CONFIG GET <pattern>` / `CONFIG SET <param> <value> [...]` | GET: flat array in RESP2, map in RESP3. SET: `+OK`, all-or-nothing. Params: `maxmemory` (bytes or units like `100mb`; 0 = unlimited), `maxclients` (applies at runtime) |
| `LPUSH` | `LPUSH key value [value ...]` | `:<new length>` — values prepended in command order |
| `BRPOP` | `BRPOP key [key ...] timeout` | blocking pop from the tail: `*2 [key value]`, or null array (`*-1` RESP2 / `_` RESP3) on timeout. `timeout` is seconds (floats ok), `0` blocks forever |
| `LREM` | `LREM key count value` | `:<removed>` — count>0 head→tail, <0 tail→head, 0 removes all |
| `SADD` | `SADD key member [member ...]` | `:<added>` — duplicates ignored |
| `ZADD` | `ZADD key score member [score member ...]` | `:<added>` — updating an existing member counts 0 |
| `ZRANGEBYSCORE` | `ZRANGEBYSCORE key min max` | array of members by ascending score; bounds accept `-inf`/`+inf` and `(n` for exclusive |

The list/set/sorted-set commands are the basic tier Celery's redis transport (kombu) needs: `LPUSH` to enqueue, `BRPOP` to consume, `LREM` to ack, `SADD` for `celery.revoked`, `ZADD`/`ZRANGEBYSCORE` for the unacked visibility-timeout index. `probe_commands.py` in `demo/` exercises each one.

Commands are case-insensitive. Both RESP array framing (what real clients send) and inline commands (what you type into `nc`/telnet: `SET foo bar`) are accepted. `CLIENT SETINFO` is required because redis-py ≥ 5 sends it twice on every connect; it accepts any arguments and does nothing.

**Protocols:** connections start in RESP2 (the default). `HELLO 3` switches a single connection to RESP3 — reply encoding changes for that connection only: null bulk strings become `_` and `HELLO` replies become maps (`%7`). `HELLO 2` switches back. `HELLO 4` (or junk) replies Redis's exact `NOPROTO unsupported protocol version` without changing the protocol. AUTH matches real Redis with no requirepass configured: the default user is nopass, so `HELLO <proto> AUTH default <anything>` succeeds, other usernames reply `WRONGPASS`, and incomplete AUTH or unsupported options (like `SETNAME`) reply Redis's `Syntax error in HELLO option '...'`.

**Not implemented (intentionally):** `SETEX`/`PSETEX`, `NX`/`XX`, `MSET`/`MGET`, `INCR` family, `EXPIRE`/`TTL`, `SCAN`, `SELECT`, `QUIT`, `BLPOP`/`RPOP`/`LPOP`/`LLEN`, `SREM`/`SMEMBERS`, `ZREM`, persistence, active expiry, eviction policies (when `maxmemory` is exceeded, writes are rejected with `-OOM` — Redis's default `noeviction` policy), RESP3 beyond maps/nulls (sets, doubles, booleans, verbatim strings, push messages).

## Run it

```powershell
cd cache_server
go run .                        # listens on localhost:6379
go run . -addr localhost:6380   # pick another address/port
go run . -maxclients 500        # cap simultaneous connections (default 1000; also CONFIG SET maxclients at runtime)
go run . -maxmemory 100mb       # memory budget for string values (units: b/k/kb/m/mb/g/gb; default 0 = unlimited)
go run . -log=false             # turn off per-command traffic logging (on by default)
```

Connections beyond `-maxclients` are rejected immediately with `-ERR max number of clients reached`. Writes over `-maxmemory` are rejected with `-OOM command not allowed when used memory > 'maxmemory'`.

**Traffic logging:** with `-log` on (the default), every connected client's exchange is printed to the terminal, tagged with its connection id so you can follow one client:

```
2026/11/08 18:02:22.565858 [conn 1] connected from 127.0.0.1:57274
2026/11/08 18:02:22.566365 [conn 1] --> SET greeting "hello world"
2026/11/08 18:02:22.566450 [conn 1] <-- +OK
2026/11/08 18:02:22.566450 [conn 1] --> GET greeting
2026/11/08 18:02:22.566450 [conn 1] <-- $11 hello world
2026/11/08 18:02:22.566450 [conn 1] disconnected (127.0.0.1:57274)
```

`-->` is the inbound command, `<--` the reply (rendered RESP-style: `+`/`-`/`:`/`$`/`*`/`%`). Long values and big collections are truncated to one line so Celery-sized payloads stay readable. Logging is off in the test binary (tests call `serve()` directly, never `main()`), so test output stays clean.

Connect with any Redis client:

```
redis-cli SET foo bar         # OK
redis-cli GET foo             # "bar"
redis-cli DEL foo             # (integer) 1
```

No `redis-cli`? `nc localhost 6379` and type `GET foo` — inline commands are accepted.

**Port caveat:** the default (6379) is the standard Redis port. If you run the `docker-compose.yml` in this repo (which starts a real `redis:7-alpine` on 6379) at the same time, they collide — pass `-addr localhost:6380` to the Go server.

## Wiring up the Django demo

`demo/` uses Django's built-in `django.core.cache.backends.redis.RedisCache` (configured via `REDIS_URL`, default `redis://localhost:6379/0`) — only `redis-py` is needed, no `django-redis`. On database 0 redis-py never sends `SELECT`, so the trimmed command set covers the connection handshake and basic cache get/set.

Supported in the demo with this build:

- `cache.get(key)` → `GET`
- `cache.set(key, value)` → `SET key value EX <timeout>` (default timeout 300 s — hence `SET` supports `EX`)

Optional RESP3: point the demo at `redis://localhost:6379/0?protocol=3` — redis-py will send `HELLO 3` on connect and the server replies in RESP3 (missing keys come back as `_` instead of `$-1`). Note redis-py 8 defaults to RESP3 on its own, so `CONFIG GET` must answer with a map (`%`) on those connections — it does.

Will raise errors against this build (commands removed): `cache.get_many`/`set_many` (MSET/MGET), `cache.incr`/`decr` (INCR), `cache.touch` (EXPIRE), `cache.add`/`get_or_set` (SET NX), and `cache.delete_many` still works (DEL is variadic) — as does `cache.delete` (DEL), `cache.has_key` (EXISTS), and `cache.clear` (FLUSHDB).

Celery: `demo/probe_commands.py` fires the exact command sequences Celery/kombu, RQ, and Channels use. The Celery tier (`LPUSH`, `BRPOP`, `LREM`, `SADD`, `ZADD`, `ZRANGEBYSCORE`) passes against this build; the RQ/Channels tier (hash commands, `BLPOP`, `PUBLISH`, …) still fails by design.

The old HTTP/JSON demo client (`go_client.py`) and its custom cache backend (`backends.py`) were deleted along with the HTTP API. Also note: the demo view in `demo/base/views.py` caches a lazy QuerySet, so a cache hit still evaluates against the database unless the queryset is serialized first — worth fixing when you test.

## Project layout

| Path | Responsibility |
|---|---|
| `cache_server/main.go` | `Config` (maxmemory/maxclients, runtime-mutable), `serve` accept loop with the maxclients cap, per-connection handler, `main()` with `-addr`/`-maxclients`/`-maxmemory`/`-log` flags |
| `cache_server/cache_store.go` | `Store` interface and `MemoryStore` (strings with TTLs + lists/sets/sorted sets, used-memory accounting, `ErrOOM`/`ErrWrongType`) |
| `cache_server/parser.go` | RESP codec: `readCommand` decoder, `writeReply` encoder, `Reply` type (incl. null arrays for BRPOP timeouts) |
| `cache_server/logging.go` | Optional traffic logger (`-log`): `[conn N]`-tagged per-command `-->`/`<--` lines, with one-line sanitizing and truncation of large values/collections |
| `cache_server/commands.go` | Command/arity table, `dispatch`, per-command handlers, `parseMemSize`, CONFIG GET/SET |
| `cache_server/pubsub.go` | `PubSub` channel registry: subscribe/unsubscribe/publish and per-connection cleanup |
| `demo/` | Django demo app (RedisCache + Celery) and `probe_commands.py` ecosystem probes |
| `cache_server/cache_store_test.go` | Store unit tests: strings/TTL/expiry, lists/sets/sorted sets, OOM budget, wrong-type guards, used-memory accounting |
| `cache_server/commands_test.go` | Command-table/dispatch tests, handler tests, `parseMemSize`/`parseScoreBound`, CONFIG GET/SET |
| `cache_server/parser_test.go` | RESP codec tests: `writeReply` encoder and `readCommand` decoder, including limits and truncation |
| `cache_server/pubsub_test.go` | Pub/sub unit tests (subscribe/unsubscribe/publish/cleanup) plus raw-socket delivery tests |
| `cache_server/logging_test.go` | Traffic-log formatting: sanitizing, quoting, arg/reply summaries, nil-logger silence |
| `cache_server/e2e_test.go` | Raw-socket end-to-end tests asserting exact wire bytes |
| `IMPLEMENTATION_GUIDE.md` | Learning guide: step-by-step implementation plans for EXEC/MULTI, the SET options refactor, and GET extensions |
| `security_report.md` | Full security audit (2026-11): findings with severities + remediation roadmap |
| `docs.md` | Rationale behind the architectural decisions |

## Testing

```powershell
cd cache_server
go test ./...        # unit + e2e (needs no external services)
go vet ./...
```

`go test -race ./...` requires a C toolchain (cgo) and is unavailable on machines without gcc — the concurrent tests still exercise the same paths without the detector.

## Known gaps

The remaining gap list — everything from missing commands to persistence, replication, and professional tooling — is in `docs.md` (§ Deferred / out of scope) and the git history.

- `cache_server/dockerfile` is empty — no container build is wired up yet.
- No idle-connection timeouts or graceful shutdown (SIGINT) — connections are cleaned up on close, but an abandoned idle connection currently lives until the client goes away.
- No active expiry: expired keys are removed lazily when touched, so an expired-but-never-read key still occupies memory until `DEL` or server restart. Lists/sets/zsets have no TTL support at all.
- `BRPOP` blocks by polling every 50 ms (Redis wakes blocked clients from its event loop), and `BRPOP ... 0` (block forever) leaks the handler goroutine if the client vanishes silently — kombu always sends a finite timeout.
- `used` memory accounting covers string values only; list/set/zset elements are not counted against `maxmemory`.
- `WRONGTYPE` is enforced one way: list/set/zset commands reject live string keys, but `GET` on a list key returns null instead of a WRONGTYPE error.
