from django.core.cache import cache
from django.core.management import call_command
from django.core.management.base import CommandError
from django.test import TestCase, override_settings

from base.models import Author, Post
from base.serializers import AuthorSerializer, PostSerializer

LOCMEM_CACHES = {
    "default": {
        "BACKEND": "django.core.cache.backends.locmem.LocMemCache",
        "LOCATION": "test-locmem",
    }
}


class ModelTest(TestCase):
    def test_author_and_post_creation(self):
        author = Author.objects.create(name="Jane")
        post = Post.objects.create(author=author, title="T", content="C")
        self.assertEqual(post.author, author)
        self.assertEqual(Post.objects.count(), 1)

    def test_author_cascade_deletes_posts(self):
        author = Author.objects.create(name="Jane")
        Post.objects.create(author=author, title="T", content="C")
        author.delete()
        self.assertEqual(Post.objects.count(), 0)


class SerializerTest(TestCase):
    def test_post_serializer_shape(self):
        author = Author.objects.create(name="Jane")
        post = Post.objects.create(author=author, title="Hello", content="World")
        data = PostSerializer(post).data
        self.assertEqual(data["id"], post.id)
        self.assertEqual(data["title"], "Hello")
        self.assertEqual(data["content"], "World")
        self.assertEqual(data["author"], author.id)

    def test_author_serializer_shape(self):
        author = Author.objects.create(name="Jane")
        self.assertEqual(AuthorSerializer(author).data, {"id": author.id, "name": "Jane"})


@override_settings(CACHES=LOCMEM_CACHES)
class PostViewTest(TestCase):
    def setUp(self):
        cache.clear()

    def test_empty_database_returns_empty_list(self):
        response = self.client.get("/api/posts/")
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json(), [])

    def test_returns_all_posts(self):
        author = Author.objects.create(name="Jane")
        for i in range(3):
            Post.objects.create(author=author, title=f"Post {i}", content=f"C{i}")
        response = self.client.get("/api/posts/")
        self.assertEqual(response.status_code, 200)
        data = response.json()
        self.assertEqual(len(data), 3)
        titles = {item["title"] for item in data}
        self.assertEqual(titles, {"Post 0", "Post 1", "Post 2"})
        for item in data:
            self.assertEqual(item["author"], author.id)

    def test_view_populates_the_cache_key(self):
        author = Author.objects.create(name="Jane")
        Post.objects.create(author=author, title="T", content="C")
        self.assertIsNone(cache.get("posts_query"))
        response = self.client.get("/api/posts/")
        self.assertEqual(response.status_code, 200)
        self.assertIsNotNone(cache.get("posts_query"))


class PopulateDbCommandTest(TestCase):
    def test_populates_requested_number_of_posts(self):
        call_command("populate_db", posts=7)
        self.assertEqual(Post.objects.count(), 7)
        self.assertEqual(Author.objects.count(), 7)
        for i in range(1, 8):
            post = Post.objects.get(title=f"Post {i}")
            self.assertEqual(post.author.name, f"Author {i}")
            self.assertEqual(post.content, f"Content of post {i}")

    def test_posts_zero_is_rejected(self):
        with self.assertRaises(CommandError):
            call_command("populate_db", posts=0)
        self.assertEqual(Post.objects.count(), 0)

    def test_negative_posts_is_rejected(self):
        with self.assertRaises(CommandError):
            call_command("populate_db", posts=-5)
        self.assertEqual(Post.objects.count(), 0)

    def test_missing_posts_argument_is_rejected(self):
        with self.assertRaises(CommandError):
            call_command("populate_db")
        self.assertEqual(Post.objects.count(), 0)
