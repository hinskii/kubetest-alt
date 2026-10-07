# Object storage

kubetest keeps run logs, scraped artifacts and each run's `result.json` in
one bucket, under `runs/<namespace>/<runUID>/` (`pkg/storage.RunKeys`). Three
parties use it:

- the **operator** — reads `result.json` (the verdict) and uploads streamed
  log chunks;
- the **API server** — serves logs and artifacts, deletes runs from history;
- the **wrapper in every test pod** — uploads artifacts and `result.json`.

So every one of them, including test pods in every namespace that runs
tests, needs access to the bucket.

Two backends, chosen with `storage.type`:

| `type` | Store | Credentials |
|--------|-------|-------------|
| `s3`   | Any S3-compatible store: AWS S3, MinIO, Ceph RGW, Cloudflare R2, … | a Secret with `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`, or the AWS credential chain (IRSA on EKS) |
| `gcs`  | Google Cloud Storage, native JSON API | Application Default Credentials — Workload Identity on GKE |

Without `storage.type` there is no object storage: no artifacts, no stored
logs, and verdicts come from pod state only.

## S3 (incl. MinIO)

```yaml
storage:
  type: s3
  bucket: kubetest-artifacts
  s3:
    endpoint: minio.storage.svc:9000   # empty = AWS S3
    useSSL: false                      # true for AWS / TLS endpoints
    region: ""                         # AWS region when needed
    secretName: s3-creds
```

`s3-creds` must exist in the release namespace **and in every namespace
that runs tests** (the wrapper gets it via `envFrom`):

```sh
kubectl -n <ns> create secret generic s3-creds \
  --from-literal=AWS_ACCESS_KEY_ID=... --from-literal=AWS_SECRET_ACCESS_KEY=...
```

With `secretName` empty, every component uses the AWS credential chain —
on EKS annotate `serviceAccounts.*.annotations` with
`eks.amazonaws.com/role-arn` and give test pods an IRSA-bound service
account (below).

## GCS (Workload Identity)

```yaml
storage:
  type: gcs
  bucket: my-project-kubetest
serviceAccounts:
  operator:
    annotations:
      iam.gke.io/gcp-service-account: kubetest@my-project.iam.gserviceaccount.com
  apiserver:
    annotations:
      iam.gke.io/gcp-service-account: kubetest@my-project.iam.gserviceaccount.com
```

On the Google side (one GSA for everything is simplest):

```sh
gcloud iam service-accounts create kubetest
gcloud storage buckets add-iam-policy-binding gs://my-project-kubetest \
  --member=serviceAccount:kubetest@my-project.iam.gserviceaccount.com \
  --role=roles/storage.objectAdmin
# Allow each Kubernetes SA to act as the GSA (operator, API server, and the
# test-pod SA in every namespace that runs tests):
gcloud iam service-accounts add-iam-policy-binding kubetest@my-project.iam.gserviceaccount.com \
  --role=roles/iam.workloadIdentityUser \
  --member="serviceAccount:my-project.svc.id.goog[<namespace>/<k8s-sa>]"
```

`?presign=1` on artifact downloads additionally needs signing rights
(`roles/iam.serviceAccountTokenCreator` on the GSA itself); the default
streaming download does not.

`storage.gcs.endpoint` exists only for emulators (fake-gcs-server) and
disables authentication.

## Test pods

The wrapper runs in the test pod with the pod's Kubernetes service
account. With GCS (or S3 via IRSA) that account must be bound to cloud
credentials. Create it per namespace and set it on Tests — most simply in
a shared TestTemplate so every Test using it inherits it:

```yaml
apiVersion: tests.kubetest.io/v1alpha1
kind: TestTemplate
metadata: {name: team-defaults}
spec:
  pod:
    serviceAccountName: kubetest-runner   # annotated for Workload Identity / IRSA
```

## Retention

With `postgresql.dsn` set, the operator removes finished runs older than
`retention.days` (default 30) every hour: for each monthly history
partition past the window it deletes every run's objects
(`runs/<namespace>/<uid>/` — logs, artifacts, `result.json`), then drops the
partition. Removal is per month, so a run lives up to a month longer than
`retention.days`. If deleting objects fails, the partition is kept and the
next pass retries — nothing is orphaned. Audit-log entries older than the
window go too. `retention.days: 0` keeps everything.

Without Postgres the operator has no list of runs to expire: give the
bucket a lifecycle rule instead (S3 `Expiration` on prefix `runs/`, GCS
`Delete` with `age` and `matchesPrefix: [runs/]`).

## Finished TestRuns

With `postgresql.dsn` set, a finished TestRun is deleted from the cluster
`runs.finishedTTL` (default `1h`) after it finished, once it is in run
history — etcd holds the runs in progress and the last hour, not every run
ever. The run stays in Control Center and the API (`GET /runs`,
`/runs/{name}` — found by name or UID) with its logs, artifacts and test
cases until retention, and "Run again" works from there. The hour lets
`kubectl wait --for=jsonpath='{.status.phase}'=passed testrun/<name>` and
CI scripts read the result first. Composite runs go as a whole: children
are never deleted while their parent runs, and leave with it.
`runs.finishedTTL: 0` keeps finished TestRuns; without Postgres they are
always kept (the TestRun is the only record).

## Verification

- `pkg/storage/backends_integration_test.go` runs one conformance suite
  against a real MinIO and against fake-gcs-server (`make test-integration`).
- The kind e2e runs both backends in CI (`E2E_STORAGE=s3|gcs`).
- Not covered by CI: real GCS / AWS (credentials). A first install on a real
  bucket should run one Test end to end and check its logs and artifacts in
  the GUI/API.
