from locust import HttpUser, task, constant


class FixtureUser(HttpUser):
    wait_time = constant(0.1)

    @task
    def root(self):
        self.client.get("/")

    @task
    def missing(self):
        self.client.get("/missing")
