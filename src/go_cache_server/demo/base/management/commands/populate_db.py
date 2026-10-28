from django.core.management.base import BaseCommand, CommandError

from base.models import Author, Post


class Command(BaseCommand):
    help = "Populate the database with a given number of posts (--posts N)."

    def add_arguments(self, parser):
        parser.add_argument(
            "--posts",
            type=int,
            required=True,
            help="Number of posts to create",
        )

    def handle(self, *args, **options):
        count = options["posts"]
        if count < 1:
            raise CommandError("--posts must be a positive integer")

        authors = [Author(name=f"Author {i}") for i in range(1, count + 1)]
        Author.objects.bulk_create(authors)

        posts = [
            Post(author=author, title=f"Post {i}", content=f"Content of post {i}")
            for i, author in enumerate(authors, start=1)
        ]
        Post.objects.bulk_create(posts)

        self.stdout.write(
            self.style.SUCCESS(f"Created {count} posts with {count} authors.")
        )
