# Selenium — example-only, awaiting `services` runtime support

Selenium tests need a browser process alongside the test runner:

- The test container (pytest + selenium-python bindings, or Java + JUnit +
  selenium-java, etc.) runs the assertions.
- The browser container (chromedriver / geckodriver + a real Chromium /
  Firefox) receives the WebDriver commands.

kubetest-alt's schema supports this via `spec.services{}` (mirrors the
Testkube TestWorkflow feature) — but the `services` runtime is on the
backlog. Until that lands there's no template that produces a
correct end-to-end Selenium Test.

## Target shape (once `services` runtime ships)

```yaml
apiVersion: tests.kubetest.io/v1alpha1
kind: Test
metadata:
  name: selenium-example
  namespace: default
  labels:
    kubetest.io/tool: selenium
spec:
  # The test runner — a JVM/Python/Node image with selenium bindings.
  container:
    image: python:3.13-slim
    command: ["sh", "-c"]
    args:
      - |
        pip install --quiet selenium==4.26.0 pytest==9.1.1 && \
          pytest --junit-xml=/data/repo/results/junit.xml \
                 /data/repo/tests/
  # Sidecar-like services declared here run as separate pods; the test
  # container discovers them via services.<name>.<index>.ip.
  services:
    chrome:
      image: selenium/standalone-chrome:130.0
      readinessProbe:
        httpGet: { path: /status, port: 4444 }
      timeout: 60s
  artifacts:
    paths:
      - "results/**/*.xml"
  config:
    testsDir:
      type: string
      default: "selenium/"
```

The test code reads the WebDriver URL from an env var:

```python
# tests/test_login.py
import os
from selenium import webdriver

def test_login():
    url = os.environ["WEBDRIVER_URL"]  # e.g. http://services.chrome.0.ip:4444/wd/hub
    driver = webdriver.Remote(command_executor=url, options=webdriver.ChromeOptions())
    # ...
```

## Why no template

- `services{}` compilation isn't wired end-to-end in the operator yet
  — `TestSpec.Services` accepts the shape (step-02 schema) but the
  compiler doesn't create the sidecar Job/Pod tree that `spec.services`
  demands. A template that ships today wouldn't produce a working
  Selenium run.
- When the runtime lands, this doc's example becomes the reference —
  no template rework needed; consumers copy the shape into their
  Test manifest.
- Alternative today: run selenium-standalone-chrome as a separate
  Deployment in the cluster, hard-code its Service URL in the test
  args. That works with the pytest template right now — it just
  isn't reproducible from the Test CR alone.
