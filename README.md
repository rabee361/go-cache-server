# go-cache-server

An in-memory cache server that speaks a Redis-compatible subset of **RESP** (the Redis serialization protocol) over raw TCP. No HTTP, no JSON — any RESP client (e.g. `redis-cli`, `redis-py`) can talk to it.

## Supported commands

| Command | Syntax | Reply |
|---|---|---|
| `PING` | `PING [msg]` | `+PONG` (or echoes `msg` as a bulk string) |
| `SET` | `SET key value` | `+OK` |
| `GET` | `GET key` | bulk string, or `$-1` (null) if missing |
| `DEL` | `DEL key [key ...]` | `:<count>` — number of keys actually removed |
| `EXISTS` | `EXISTS key [key ...]` | `:<count>` — number of keys present |
| `QUIT` | `QUIT` | `+OK`, then the server closes the connection |
| `READY` | `READY` | `+READY` or `-ERR ...` — **non-standard extension**, see below |

Commands are case-insensitive. Both RESP array framing (what real clients send) and inline commands (what you type into `nc`/telnet: `SET foo bar`) are accepted.

**Not implemented** (and not planned): persistence, TTL/expiry, `SELECT`/multiple DBs, `MGET`/`MSET`, pub/sub, `MULTI`/transactions, RESP3, AUTH, clustering.

### `READY` — non-standard extension

`READY` performs a store self-test (set → get-verify → delete a temporary key) and replies `+READY` or an error. It exists to preserve the old HTTP `/health/ready` readiness probe. Use standard `PING` for health checks that must work with any Redis client.

## Run it

```powershell
cd cache_server
go run .                     # listens on localhost:6379
go run . -addr localhost:6380   # pick another address/port
```

Connect with any Redis client:

```
redis-cli PING                # PONG
redis-cli SET foo bar         # OK
redis-cli GET foo             # "bar"
redis-cli DEL foo             # (integer) 1
```

No `redis-cli`? `nc localhost 6379` and type `PING` — inline commands are accepted.

**Port caveat:** the default (6379) is the standard Redis port. If you run the `docker-compose.yml` in this repo (which starts a real `redis:7-alpine` on 6379) at the same time, they collide — pass `-addr localhost:6380` to the Go server.

## Breaking change: the HTTP/JSON API is gone

This server previously exposed `POST /cache/set`, `GET /cache/get|exists`, `DELETE /cache/delete`, and `/health` endpoints returning JSON envelopes. Those endpoints, the JSON response format, and port 8080 **no longer exist** — the wire protocol is RESP and the default port is 6379.

**Follow-up (not done):** `demo/demo/go_client.py` and its Django cache backend in `demo/demo/backends.py` still speak the old HTTP/JSON API and will break against this server. The Django demo also has `django-redis` configured via `REDIS_URL` (`redis://localhost:6379/0`), which is unaffected. Migrating `go_client.py` (e.g. to `redis-py`, which is wire-compatible with this subset) is left as a separate task.

## Project layout

| Path | Responsibility |
|---|---|
| `cache_server/main.go` | `MemoryStore` + `Store` interface, `serve` accept loop, per-connection handler, `main()` with the `-addr` flag |
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

- `cache_server/dockerfile` is empty — no container build is wired up yet.
- No idle-connection timeouts or graceful shutdown (SIGINT) — connections are cleaned up on close, but an abandoned idle connection currently lives until the client goes away.
