from django.core.cache import cache
from django.test import TestCase, override_settings
from django.urls import reverse

from base.models import Author, Post

LOCMEM_CACHES = {
    "default": {
        "BACKEND": "django.core.cache.backends.locmem.LocMemCache",
        "LOCATION": "template-view-tests",
    }
}


@override_settings(CACHES=LOCMEM_CACHES, DEBUG=False)
class PostsTemplateViewTest(TestCase):
    @classmethod
    def setUpTestData(cls):
        author = Author.objects.create(name="Author 1")
        Post.objects.bulk_create(
            Post(author=author, title=f"Post {i}", content=f"Content {i}")
            for i in range(1, 26)
        )

    def setUp(self):
        cache.clear()

    def test_simple_view_renders_all_posts(self):
        response = self.client.get(reverse("posts_simple"))
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.context["posts"]), 25)
        self.assertContains(response, "<h2>Post 1</h2>")
        self.assertContains(response, "<h2>Post 25</h2>")
        self.assertContains(response, "by Author 1")

    def test_simple_view_empty_database(self):
        Post.objects.all().delete()
        response = self.client.get(reverse("posts_simple"))
        self.assertEqual(response.status_code, 200)
        self.assertContains(response, "No posts yet.")

    def test_paginated_view_first_page(self):
        response = self.client.get(reverse("posts_paginated"))
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.context["page_obj"]), 10)
        self.assertEqual(response.context["page_obj"].number, 1)
        self.assertContains(response, "Page 1 of 3")
        self.assertContains(response, "Next")
        self.assertNotContains(response, "Previous")

    def test_paginated_view_last_page(self):
        response = self.client.get(reverse("posts_paginated"), {"page": 3})
        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.context["page_obj"]), 5)
        self.assertEqual(response.context["page_obj"].number, 3)
        self.assertContains(response, "Page 3 of 3")
        self.assertContains(response, "Previous")
        self.assertNotContains(response, "Next")

    def test_paginated_view_out_of_range_page_404s(self):
        response = self.client.get(reverse("posts_paginated"), {"page": 99})
        self.assertEqual(response.status_code, 404)

    def test_simple_view_is_cached_for_30_seconds(self):
        with self.assertNumQueries(1):
            first = self.client.get(reverse("posts_simple"))
        self.assertEqual(first["Cache-Control"], "max-age=30")
        with self.assertNumQueries(0):
            second = self.client.get(reverse("posts_simple"))
        self.assertEqual(second.content, first.content)

    def test_paginated_view_caches_each_page_separately(self):
        self.client.get(reverse("posts_paginated"), {"page": 1})
        self.client.get(reverse("posts_paginated"), {"page": 2})
        with self.assertNumQueries(0):
            self.client.get(reverse("posts_paginated"), {"page": 1})
        with self.assertNumQueries(0):
            self.client.get(reverse("posts_paginated"), {"page": 2})
