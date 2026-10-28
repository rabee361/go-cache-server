# go-cache-server

An in-memory cache server that speaks a Redis-compatible subset of **RESP** (the Redis serialization protocol) over raw TCP. No HTTP, no JSON — any RESP client (e.g. `redis-cli`, `redis-py`) can talk to it.

The server is intentionally minimal: it implements exactly what a Django project needs for basic queryset caching via `django.core.cache.backends.redis.RedisCache` (`cache.get` / `cache.set`), plus `DEL` to clear a bad cached value and `HELLO` for optional RESP3. Everything else was removed on purpose and can be grown back on demand.

## Supported commands

| Command | Syntax | Reply |
|---|---|---|
| `SET` | `SET key value [EX sec\|PX ms]` | `+OK` |
| `GET` | `GET key` | bulk string, or null (`$-1` in RESP2, `_` in RESP3) if missing/expired |
| `DEL` | `DEL key [key ...]` | `:<count>` — number of keys actually removed |
| `HELLO` | `HELLO [2\|3]` | server info (flat array in RESP2, map in RESP3); `HELLO 3` switches the connection to RESP3 |
| `CLIENT` | `CLIENT SETINFO lib-name lib-ver` | `+OK` — accepts the redis-py handshake |

Commands are case-insensitive. Both RESP array framing (what real clients send) and inline commands (what you type into `nc`/telnet: `SET foo bar`) are accepted. `CLIENT SETINFO` is required because redis-py ≥ 5 sends it twice on every connect; it accepts any arguments and does nothing.

**Protocols:** connections start in RESP2 (the default). `HELLO 3` switches a single connection to RESP3 — reply encoding changes for that connection only: null bulk strings become `_` and `HELLO` replies become maps (`%7`). `HELLO 2` switches back. `HELLO 4` (or junk) replies Redis's exact `NOPROTO unsupported protocol version` without changing the protocol. `HELLO ... AUTH` fails because no password is configured; `SETNAME` is not supported.

**Not implemented (intentionally):** `PING`, `SETEX`/`PSETEX`, `NX`/`XX`, `MSET`/`MGET`, `INCR` family, `EXPIRE`/`TTL`, `FLUSHDB`, `SCAN`, `SELECT`, `EXISTS`, `QUIT`, persistence, active expiry, eviction, RESP3 beyond maps/nulls (sets, doubles, booleans, verbatim strings, push messages).

## Run it

```powershell
cd cache_server
go run .                     # listens on localhost:6379
go run . -addr localhost:6380   # pick another address/port
```

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

Optional RESP3: point the demo at `redis://localhost:6379/0?protocol=3` — redis-py will send `HELLO 3` on connect and the server replies in RESP3 (missing keys come back as `_` instead of `$-1`).

Will raise errors against this build (commands removed): `cache.get_many`/`set_many` (MSET/MGET), `cache.incr`/`decr` (INCR), `cache.touch` (EXPIRE), `cache.clear` (SCAN+DEL or FLUSHDB), and `cache.delete_many` still works (DEL is variadic) — as does `cache.delete` (DEL).

The old HTTP/JSON demo client (`go_client.py`) and its custom cache backend (`backends.py`) were deleted along with the HTTP API. Also note: the demo view in `demo/base/views.py` caches a lazy QuerySet, so a cache hit still evaluates against the database unless the queryset is serialized first — worth fixing when you test.

## Project layout

| Path | Responsibility |
|---|---|
| `cache_server/main.go` | `MemoryStore` (single map, per-key TTLs, lazy expiry) + `Store` interface, `serve` accept loop, per-connection handler, `main()` with the `-addr` flag |
| `cache_server/parser.go` | RESP codec: `readCommand` decoder, `writeReply` encoder, `Reply` type |
| `cache_server/commands.go` | Command/arity table, `dispatch`, per-command handlers |
| `cache_server/main_test.go` | Store unit tests + codec/dispatch/handler tests |
| `cache_server/e2e_test.go` | Raw-socket end-to-end tests asserting exact wire bytes |
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
- No active expiry: expired keys are removed lazily when touched, so an expired-but-never-read key still occupies memory until `DEL` or server restart.
