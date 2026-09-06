import requests
import pickle 
import base64


class GoCacheClient:

    pickle_protocol = 2

    def __init__(self, host):
        self.host = host

    def set(self, key, value):
        value = base64.b64encode(pickle.dumps(value, self.pickle_protocol)).decode('utf-8')
        
        res = requests.post(
            url= f"{self.host}/cache/set",
            data= {"key": key, "value": value}
        )

    def get(self, key):
        res = requests.get(
            url= f"{self.host}/cache/get",
            params= {"key": key}
        )
        
        data = res.json().get("data")
        if not data:
            return None
            
        if data.get("value"):
            try:
                return pickle.loads(base64.b64decode(data.get("value")))
            except Exception as e:
                print("Cache deserialization error:", e)
                return None
                
        return None

    def delete(self, key):
        res = requests.delete(
            url= f"{self.host}/cache/delete",
            data={"key": key}
        )
