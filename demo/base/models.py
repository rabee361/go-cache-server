from django.db import models


class Author(models.Model):
    name = models.CharField(max_length=100)

class Post(models.Model):
    author = models.ForeignKey(Author, on_delete=models.CASCADE)
    title = models.CharField(max_length=100)
    content = models.TextField()