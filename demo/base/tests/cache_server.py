import socket
import time
import unittest

from django.core.cache.backends.redis import RedisCache
from django.test import SimpleTestCase


def wait_for_port(host, port, timeout=2.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with socket.create_connection((host, port), timeout=0.5):
                return True
        except OSError:
            time.sleep(0.05)
    return False


def make_cache(host="localhost", port=6392):
    """A fresh Django RedisCache backend with its own connection pool, so each
    call opens its own connection to the server — like a separate Django app
    process would."""
    return RedisCache(f"redis://{host}:{port}/0", {})


class CacheServerTestCase(SimpleTestCase):
    """Base class: skips unless the Go cache server is already running.

    The tester is expected to start it first, e.g.:
        cache-server.exe -addr localhost:6392
    """

    host = "localhost"
    port = 6392

    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        if not wait_for_port(cls.host, cls.port):
            raise unittest.SkipTest(
                f"Go cache server not running on {cls.host}:{cls.port}; "
                f"start it first: cache-server.exe -addr {cls.host}:{cls.port}"
            )
