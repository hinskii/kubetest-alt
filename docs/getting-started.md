# Getting started — kubetest on your machine

A throwaway kind cluster with the whole platform, built from this
checkout, to try things out: write Tests, run them, watch them in Control
Center, break things.

Needs: Docker, [kind](https://kind.sigs.k8s.io), kubectl, helm, Go.

```sh
make dev-up        # ~5 min the first time (builds the images)
make dev-ui        # Control Center → http://localhost:8090, you are dev@local (admin)
```

`make dev-up` is safe to re-run. `make dev-down` deletes the cluster.

## What's running

| Namespace | What |
|---|---|
| `kubetest-alt` | the operator, the API server, Control Center (chart `kt`), MinIO (logs, artifacts), Postgres (run history) |
| `demo` | the tool catalog (TestTemplates) and one sample Test per tool, plus `target`, the HTTP service the samples test |

```sh
kubectl get tests,testruns -n demo
kubectl get testtemplates -n demo
```

## Run something

From Control Center: a Test → **Run test**. From kubectl:

```sh
kubectl create -n demo -f - <<'EOF'
apiVersion: tests.kubetest.io/v1alpha1
kind: TestRun
metadata: {generateName: sample-k6-}
spec: {testRef: sample-k6}
EOF
kubectl get testruns -n demo -w          # queued → running → passed/failed
```

From the CLI (docs/cli.md):

```sh
make install-cli && export PATH="$PATH:$(go env GOPATH)/bin"
kubectl kubetest run sample-k6 -n demo --follow    # logs live, exit code = verdict
kubectl kubetest runs -n demo
```

The samples clone their projects from GitHub (this repository), so the
cluster needs internet; a tool's first run also pulls its image (Cypress,
Playwright and ZAP are big).

## Write your own Test

The quickest way: Control Center → **New test**, a wizard over the tool
catalog (docs/control-center.md) — it shows the Test's YAML at the end.
By hand: a Test is an image + a command (CLAUDE.md §10). Smallest one — a
k6 script inline, with a parameter:

```yaml
apiVersion: tests.kubetest.io/v1alpha1
kind: Test
metadata:
  name: hello-k6
  namespace: demo
spec:
  use: [k6]                       # the catalog template: image, report, metrics, live view
  config:
    script: {type: string, default: hello.js}
    vus: {type: integer, default: "2"}
  container:
    env:                          # {{ }} works in args and env, not in file contents
      - {name: VUS, value: "{{ config.vus }}"}
  content:
    files:
      - path: repo/hello.js       # lands at /data/repo/hello.js
        content: |
          import http from 'k6/http';
          import { check, sleep } from 'k6';
          export const options = { vus: Number(__ENV.VUS), duration: '30s' };
          export default function () {
            check(http.get('http://target:8000/'), { 'status 200': (r) => r.status === 200 });
            sleep(0.5);
          }
```

```sh
kubectl apply -f hello-k6.yaml
kubectl kubetest run hello-k6 -n demo --config vus=5 --follow
```

Things to try next, all in Control Center afterwards:

- **Schedule:** `spec.schedule: "*/5 * * * *"` (UTC; `CRON_TZ=Europe/Warsaw …`).
- **A failing threshold:** `thresholds: { checks: ['rate==1'] }` and a
  check that fails → verdict `failed`, exit code 1 from the CLI.
- **From git:** `content.git: {uri: https://github.com/…, revision: main, paths: [k6]}`
  — the run page shows the commit.
- **Composite:** `spec.steps` executing other Tests in sequence
  (plan/step-17-composition.md); **retry**, **services**, **parallel**
  (plan/step-19-retry-services-parallel.md).
- **Any other tool:** copy one of `config/samples/tools/*.yaml`;
  `docs/onboarding-a-tool.md` explains the template fields.
- **The live view:** a k6 run lasting a minute or more shows k6's
  dashboard on its run page.

Tests you `kubectl apply` are managed in "Git" as far as Control Center is
concerned (read-only there, runs allowed); **Duplicate** on such a Test's
page makes an editable copy. Tests made with the wizard are editable.

## Look under the hood

```sh
kubectl -n kubetest-alt logs deploy/kt-kubetest-alt-operator -f          # reconciles
kubectl describe testrun -n demo <run>                                    # status, steps, message
kubectl get jobs,pods -n demo -l kubetest.io/run-id=<run>                 # the test pod
kubectl -n kubetest-alt port-forward svc/minio 9001:9001                  # MinIO console: minioadmin / minioadmin
kubectl -n kubetest-alt exec -it deploy/postgres -- psql -U kubetest      # run history: select * from test_runs;
```

The API is behind a token (docs/security.md):

```sh
TOKEN=$(kubectl -n kubetest-alt get secret kt-kubetest-alt-api-token -o jsonpath='{.data.token}' | base64 -d)
kubectl -n kubetest-alt port-forward svc/kt-kubetest-alt-apiserver 8080:8080 &
curl -H "X-Kubetest-Token: $TOKEN" 'localhost:8080/runs?namespace=demo'
curl localhost:8080/openapi.json -H "X-Kubetest-Token: $TOKEN" | jq '.paths | keys'
```

## Change the code

```sh
make dev-reload    # rebuild operator, API server, Control Center, content-fetcher; apply CRDs; restart
```

Chart changes: `helm upgrade kt charts/kubetest-alt -n kubetest-alt -f hack/dev-values.yaml`.

## When something is off

- **A run ends in `error` with `PolicyDenied`:** the test pod asked for a
  service account, volume or privilege the platform doesn't allow
  (docs/security.md, chart `testPods`).
- **Tests in another namespace never upload logs:** that namespace needs
  the storage credentials — `make dev-namespace NS=<ns>`.
- **`ImagePullBackOff` / `ErrImagePull`:** the kind node couldn't pull a
  tool image (network, Docker Hub limits); the run ends in `error`.
- **Finished TestRuns disappear from `kubectl get testruns`:** after an
  hour they leave the cluster and stay in run history (Control Center,
  `kubectl kubetest runs`) — `runs.finishedTTL`.
