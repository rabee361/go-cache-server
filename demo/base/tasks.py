from celery import shared_task
import time

@shared_task
def send_welcome_email(user_email, name):
    time.sleep(5)
    print(f"Sent welcome email to {name} at {user_email}!")
    return f"Success: Email sent to {user_email}"
