from django.core.cache.backends.base import BaseCache


class GoCacheBackend(BaseCache):
    def __init__(self, host, *args, **kwargs):
        super().__init__(*args, **kwargs)