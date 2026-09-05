import requests


class GoCacheClient:
    def __init__(self, host):
        self.host = host

    def set(self, key, value):
        res = requests.post(
            url= f"{self.host}/cache/set",
            data= {"key": key, "value":value}
        )

    def get(self, key):
        res = requests.get(
            url= f"{self.host}/cache/get",
            data= {"key": key}
        )

    def delete(self):
        res = requests.delete(
            url= f"{self.host}/cache/delete"
        )

    # def delete_all(self):
    #     requests.get(
    #         url= f"{self.host}/cache/delete"
    #     )