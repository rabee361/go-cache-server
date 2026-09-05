from django.shortcuts import render
from rest_framework.views import APIView
from .models import Post, Author
from .serializers import *
from rest_framework.response import Response


class PostView(APIView):
    def get(self, request):
        posts = Post.objects.all()
        serializer = PostSerializer(posts, many=True)
        serializer.is_valid(raise_exception=True)
        return Response(request , serializer.data)