import threading

from .cache_server import CacheServerTestCase, make_cache


class MultipleConnectionsTest(CacheServerTestCase):
    NUM_CLIENTS = 5

    def test_five_clients_roundtrip_independently(self):
        caches = [make_cache(self.host, self.port) for _ in range(self.NUM_CLIENTS)]
        barrier = threading.Barrier(self.NUM_CLIENTS)
        errors = []

        def worker(idx, c):
            try:
                barrier.wait()
                c.set(f"conn:{idx}", f"value-{idx}")
                if c.get(f"conn:{idx}") != f"value-{idx}":
                    raise AssertionError(f"client {idx}: get after set mismatch")
                c.delete(f"conn:{idx}")
                if c.get(f"conn:{idx}") is not None:
                    raise AssertionError(f"client {idx}: key survived delete")
            except Exception as exc:  # noqa: BLE001 - collected and re-raised
                errors.append(exc)

        threads = [
            threading.Thread(target=worker, args=(i, c))
            for i, c in enumerate(caches)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        self.assertEqual(errors, [])

    def test_five_clients_share_one_store(self):
        caches = [make_cache(self.host, self.port) for _ in range(self.NUM_CLIENTS)]

        # a key written through one client is visible to every other client
        caches[1].set("shared", {"from": 1})
        for idx in range(self.NUM_CLIENTS):
            self.assertEqual(caches[idx].get("shared"), {"from": 1})

    def test_five_clients_same_key_contention(self):
        caches = [make_cache(self.host, self.port) for _ in range(self.NUM_CLIENTS)]
        barrier = threading.Barrier(self.NUM_CLIENTS)
        errors = []

        def worker(idx, c):
            try:
                barrier.wait()
                for i in range(10):
                    c.set("contended", {"client": idx, "i": i})
            except Exception as exc:  # noqa: BLE001
                errors.append(exc)

        threads = [
            threading.Thread(target=worker, args=(i, c))
            for i, c in enumerate(caches)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        self.assertEqual(errors, [])

        # the final value is one of the 50 written values, uncorrupted
        final = caches[0].get("contended")
        self.assertIn(final["client"], range(self.NUM_CLIENTS))
        self.assertIn(final["i"], range(10))

    def test_one_client_disconnecting_does_not_affect_others(self):
        caches = [make_cache(self.host, self.port) for _ in range(self.NUM_CLIENTS)]

        for c in caches:
            c.set("alive", "yes")

        # client 0 drops its connections to the server (Django's RedisCache
        # keeps the connection pools on its private client, so disconnect
        # them directly)
        for pool in caches[0]._cache._pools.values():
            pool.disconnect()

        # the remaining clients keep working on their own connections
        for idx, c in enumerate(caches[1:], start=1):
            self.assertEqual(c.get("alive"), "yes", f"client {idx} after close")
            c.set(f"post-close:{idx}", "v")
            self.assertEqual(c.get(f"post-close:{idx}"), "v")
