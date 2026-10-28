from django.core.cache import cache
from django.utils.decorators import method_decorator
from django.views.decorators.cache import cache_page
from django.views.generic import ListView
from rest_framework.views import APIView
from rest_framework.response import Response
from .models import Post, Author
from .serializers import *


class PostView(APIView):
    def get(self, request):
        posts = cache.get("posts_query")
        if not posts:
            posts_query = Post.objects.all()
            cache.set("posts_query", posts_query)
            posts = posts_query
        serializer = PostSerializer(posts, many=True)
        return Response(serializer.data)


@method_decorator(cache_page(30), name="dispatch")
class PostsSimpleView(ListView):
    model = Post
    template_name = "base/posts_simple.html"
    context_object_name = "posts"

    def get_queryset(self):
        return Post.objects.select_related("author")


@method_decorator(cache_page(30), name="dispatch")
class PostsPaginatedView(ListView):
    model = Post
    template_name = "base/posts_paginated.html"
    paginate_by = 10

    def get_queryset(self):
        return Post.objects.select_related("author")