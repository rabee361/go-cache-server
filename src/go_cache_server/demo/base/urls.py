
from django.urls import path
from .views import *


urlpatterns = [
    path('api/posts/', PostView.as_view()),
    path('posts/simple/', PostsSimpleView.as_view(), name='posts_simple'),
    path('posts/paginated/', PostsPaginatedView.as_view(), name='posts_paginated'),
]
