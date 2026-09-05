from django.core.cache.backends.base import BaseCache
from .go_client import GoCacheClient


class GoCacheBackend(BaseCache):
    def __init__(self, host, *args, **kwargs):
        self.client = GoCacheClient(host)
        super().__init__(*args, **kwargs)

    def add(self):
        pass

    def set(self, key, value):
        self.client.set(key, value)

    def get(self, key):
        return self.client.get(key)