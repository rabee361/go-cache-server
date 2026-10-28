import time

from django.core.cache import cache
from django.test import override_settings
from redis.exceptions import ResponseError

from .cache_server import CacheServerTestCase

TEST_CACHES = {
    "default": {
        "BACKEND": "django.core.cache.backends.redis.RedisCache",
        "LOCATION": "redis://localhost:6392/0",
    }
}


@override_settings(CACHES=TEST_CACHES)
class DjangoCacheUsageTest(CacheServerTestCase):
    def test_set_get_delete_roundtrip(self):
        cache.set("dk", "django-value")
        self.assertEqual(cache.get("dk"), "django-value")
        cache.delete("dk")
        self.assertIsNone(cache.get("dk"))
        # deleting a missing key is a no-op
        cache.delete("dk")

    def test_complex_python_values_roundtrip(self):
        value = {"nested": [1, 2, 3], "truthy": True, "none": None}
        cache.set("pk", value)
        self.assertEqual(cache.get("pk"), value)

        cache.set("bytes-key", b"\x00\xff binary \r\n value")
        self.assertEqual(cache.get("bytes-key"), b"\x00\xff binary \r\n value")

    def test_set_with_default_timeout(self):
        # Django's cache.set always sends SET ... EX <timeout> (default 300)
        cache.set("tk", "v")
        self.assertEqual(cache.get("tk"), "v")

    def test_expiry(self):
        cache.set("ek", "v", timeout=1)
        self.assertEqual(cache.get("ek"), "v")
        time.sleep(1.2)
        self.assertIsNone(cache.get("ek"))

    def test_overwrite(self):
        cache.set("ok", "first")
        cache.set("ok", "second")
        self.assertEqual(cache.get("ok"), "second")

    def test_versioned_keys(self):
        cache.set("vk", "v2", version=2)
        self.assertEqual(cache.get("vk", version=2), "v2")
        self.assertIsNone(cache.get("vk", version=1))

    def test_delete_many(self):
        cache.set("m1", 1)
        cache.set("m2", 2)
        cache.delete_many(["m1", "m2", "never-existed"])
        self.assertIsNone(cache.get("m1"))
        self.assertIsNone(cache.get("m2"))

    def test_cache_survives_multiple_operations_on_same_client(self):
        # a single connection must handle many sequential operations
        for i in range(20):
            cache.set(f"loop:{i}", i)
        for i in range(20):
            self.assertEqual(cache.get(f"loop:{i}"), i)

    def test_unsupported_cache_features_raise_clean_errors(self):
        cache.set("ck", 41)
        for op in (
            lambda: cache.incr("ck"),  # INCRBY
            lambda: cache.add("ak", "v"),  # SET NX
            lambda: cache.get_many(["ck"]),  # MGET
            lambda: cache.set_many({"x": 1}),  # MSET
            lambda: cache.touch("ck", 10),  # EXPIRE
            lambda: cache.clear(),  # FLUSHDB
        ):
            with self.assertRaises(ResponseError):
                op()
