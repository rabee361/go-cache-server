from rest_framework.serializers import ModelSerializer
from .models import *


class PostSerializer(ModelSerializer):
    class Meta:
        model = Post
        fields = '__all__'

class AuthorSerializer(ModelSerializer):
    class Meta:
        model = Author
        fields = '__all__'