from rest_framework.serializers import ModelSerializer
from .models import *


class PostSerializer(ModelSerializer):
    class Meta:
        model = Post

class AuthorSerializer(ModelSerializer):
    class Meta:
        model = Author