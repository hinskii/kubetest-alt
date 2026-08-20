# curl — raw Test manifest (no template)

`curl` is too trivial to justify a shared template — every curl-based
test is essentially "hit this URL, check the response". A one-liner
raw Test manifest is clearer than pulling in a template. This doc is
the reference example.

## Basic health check

```yaml
apiVersion: tests.kubetest.io/v1alpha1
kind: Test
metadata:
  name: healthcheck-api
  namespace: default
  labels:
    kubetest.io/tool: curl
spec:
  container:
    image: curlimages/curl:8.11.1
    command: ["curl"]
    args:
      # `-f` = fail-with-non-zero on HTTP 4xx/5xx (this is the whole
      # reason we bother wrapping curl at all — without -f curl exits 0
      # even on 404 or 500, and the operator would report `passed`).
      - "-fsS"
      - "-o"
      - "/dev/null"
      - "-w"
      - "http=%{http_code} time=%{time_total}s\\n"
      - "https://api.staging.example.com/healthz"
```

## Assert a specific status code

curl's `-f` treats any 2xx as success. For "must be exactly 200":

```yaml
spec:
  container:
    image: curlimages/curl:8.11.1
    command: ["sh", "-c"]
    args:
      - |
        set -eu
        code=$(curl -sS -o /dev/null -w '%{http_code}' https://api.staging.example.com/healthz)
        [ "$code" = "200" ] || { echo "got $code, want 200" >&2; exit 1; }
```

## Why no template

- curl's args ARE the test — a template with a `url` config would just
  be pushing indirection at users. Raw Tests read cleaner.
- Different curl tests have wildly different args (headers, methods,
  bodies, jq post-processing, custom cert bundles) — no shared shape
  worth crystallizing.
- Curl is on curlimages/curl at every version tag we might care about
  — no verdict processor needed, exit code with `-f` is honest.

## kubetest.io/tool label

Set `metadata.labels."kubetest.io/tool": curl` on the Test so
`kubectl get tests` groups curl runs under one column, same as
template-backed tools.
