"""Probe the Go cache server the way Django and its ecosystem actually use it.

Each case fires the exact command sequence that Django's RedisCacheClient (or
redis-py, Celery, RQ, Channels) sends, and reports PASS/FAIL. Re-run this
script after implementing commands to watch cases flip to PASS.

Usage (server must be running, e.g. cache-server.exe -addr localhost:6392):
    python probe_commands.py [host] [port]
"""

import sys

import redis
from django.core.cache.backends.redis import RedisCache

HOST = sys.argv[1] if len(sys.argv) > 1 else "localhost"
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 6392

results = []


def probe(name, fn):
    try:
        result = fn()
        results.append((name, True, repr(result)))
    except Exception as exc:  # noqa: BLE001 - probe wants every failure verbatim
        results.append((name, False, f"{type(exc).__name__}: {exc}"))


def make_cache():
    """A fresh Django RedisCache with its own pool, like a real app."""
    return RedisCache(f"redis://{HOST}:{PORT}/0", {})


# ---------------------------------------------------------------- baseline
# These should PASS today: they are the implemented command set.

def run_baseline():
    cache = make_cache()

    def set_get_delete():
        cache.set("bk", "v")
        assert cache.get("bk") == "v"
        cache.delete("bk")
        assert cache.get("bk") is None

    def delete_many():
        cache.set("m1", 1)
        cache.set("m2", 2)
        cache.delete_many(["m1", "m2", "ghost"])  # variadic DEL

    probe("baseline: set/get/delete", set_get_delete)
    probe("baseline: delete_many (variadic DEL)", delete_many)


# ---------------------------------------------- Django cache API (Tier 1)
# Each is a public cache API call; the comment shows the exact wire command.

def run_django_cache_api():
    cache = make_cache()

    probe("cache.add()            -> SET key val NX [EX]",  # Django 6.1: set(..., nx=True)
          lambda: cache.add("ak", "v"))
    probe("cache.get_or_set()     -> get + add + get (SET NX)",
          lambda: cache.get_or_set("gk", lambda: "computed"))
    probe("cache.incr()           -> EXISTS then INCRBY",
          lambda: cache.incr("ik"))
    probe("cache.get_many()       -> MGET",
          lambda: cache.get_many(["a", "b"]))
    probe("cache.set_many()       -> pipeline: MSET + EXPIRE per key",
          lambda: cache.set_many({"s1": 1, "s2": 2}))
    probe("cache.touch()          -> EXPIRE",
          lambda: cache.touch("tk", 60))
    probe("cache.has_key()        -> EXISTS",
          lambda: cache.has_key("hk"))
    probe("cache.clear()          -> FLUSHDB",
          lambda: cache.clear())
    probe("cache.incr_version()   -> get + set (works today)",
          lambda: (cache.set("vk", 1), cache.incr_version("vk")))


# ------------------------------------------------------- redis-py primitives

def run_redis_py_primitives():
    r = redis.Redis(host=HOST, port=PORT)

    probe("ping()                 -> PING",
          lambda: r.ping())
    probe("Lock.acquire()         -> SET lock token NX PX 30000",
          lambda: redis.lock.Lock(r, "lk").acquire(blocking_timeout=0.5))
    probe("expire()               -> EXPIRE",
          lambda: r.expire("ek", 30))
    probe("exists()               -> EXISTS",
          lambda: r.exists("ek"))
    probe("incr()                 -> INCRBY",
          lambda: r.incr("ik", 1))


# ---------------------------------- ecosystem consumers (Tier 2 commands)
# Celery/RQ/Channels are not installed; these are the exact commands they
# send, fired through redis-py so you can watch them hit the server.

def run_ecosystem_simulations():
    r = redis.Redis(host=HOST, port=PORT)

    probe("Celery publish        -> LPUSH queue <task>",
          lambda: r.lpush("celery", "msg"))
    probe("Celery worker wait    -> BRPOP queue 1",
          lambda: r.brpop("celery", 1))
    probe("Celery ack            -> LREM queue 0 <task>",
          lambda: r.lrem("celery", 0, "msg"))
    probe("Celery revoked        -> SADD celery.revoked <id>",
          lambda: r.sadd("celery.revoked", "tid"))
    probe("Celery visibility     -> ZADD celery.unacked <ts> <id>",
          lambda: r.zadd("celery.unacked", {"tid": 100}))
    probe("RQ enqueue job        -> HSET rq:job:<id> data ...",
          lambda: r.hset("rq:job:1", mapping={"data": "x"}))
    probe("RQ read job           -> HGETALL rq:job:<id>",
          lambda: r.hgetall("rq:job:1"))
    probe("RQ scheduler          -> ZRANGEBYSCORE rq:scheduled -inf <now>",
          lambda: r.zrangebyscore("rq:scheduled", "-inf", "+inf"))
    probe("Channels send         -> LPUSH asgi:<channel> <msg>",
          lambda: r.lpush("asgi:chan1", "msg"))
    probe("Channels receive      -> BLPOP asgi:<channel> 1",
          lambda: r.blpop("asgi:chan1", 1))
    probe("Channels group_add    -> SADD asgi:group:<g> <channel>",
          lambda: r.sadd("asgi:group:g1", "asgi:chan1"))
    probe("Channels group_send   -> SMEMBERS asgi:group:<g>",
          lambda: r.smembers("asgi:group:g1"))
    probe("Channels group_expiry -> ZADD asgi:group:<g> <ts> <channel>",
          lambda: r.zadd("asgi:group:g1", {"asgi:chan1": 999}))
    probe("Channels broadcast    -> PUBLISH asgi:<channel> <msg>",
          lambda: r.publish("asgi:chan1", "msg"))


def main():
    run_baseline()
    run_django_cache_api()
    run_redis_py_primitives()
    run_ecosystem_simulations()

    print()
    passed = sum(1 for _, ok, _ in results if ok)
    print(f"{passed}/{len(results)} cases pass")
    print()
    print("FAILURES (what to implement, in the order Django will need them):")
    for name, ok, detail in results:
        if not ok:
            print(f"  [{name}]")
            print(f"      {detail}")


if __name__ == "__main__":
    main()
