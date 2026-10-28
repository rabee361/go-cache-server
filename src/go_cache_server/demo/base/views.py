from django.shortcuts import render
from rest_framework.views import APIView
from .models import Post, Author
from .serializers import *
from rest_framework.response import Response
from django.core.cache import cache


class PostView(APIView):
    def get(self, request):
        posts = cache.get("posts_query")
        print("do we have it in the cache ?", posts)
        if not posts:
            posts_query = Post.objects.all()
            cache.set("posts_query", posts_query)
            posts = posts_query
        print("posts are  : " , posts)
        print("posts from query are  : " , Post.objects.all())

        serializer = PostSerializer(posts, many=True)
        return Response(serializer.data)