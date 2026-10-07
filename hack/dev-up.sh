#!/usr/bin/env bash
# Local kubetest on kind for trying things out (`make dev-up`):
#   kind cluster → platform images built from this checkout → MinIO
#   (logs/artifacts) + Postgres (run history) → the chart with Control
#   Center (hack/dev-values.yaml) → the tool catalog and one sample Test
#   per tool in namespace "demo".
# Re-running it is safe: it reuses the cluster and rebuilds the images.
# `make dev-ui` opens Control Center, `make dev-down` removes the cluster.
set -euo pipefail

CLUSTER="${KIND_CLUSTER:-kubetest-dev}"
NS=kubetest-alt
DEMO="${DEMO_NS:-demo}"
TAG=dev
cd "$(dirname "$0")/.."

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

for bin in kind kubectl helm docker; do
  command -v "$bin" >/dev/null || { echo "missing: $bin" >&2; exit 1; }
done

say "kind cluster $CLUSTER"
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER"
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

say "building images from this checkout"
docker build -q -f Dockerfile -t "kubetest-alt/operator:$TAG" --build-arg TARGET_BIN=cmd/operator .
docker build -q -f Dockerfile -t "kubetest-alt/apiserver:$TAG" --build-arg TARGET_BIN=cmd/apiserver .
docker build -q -f Dockerfile -t "kubetest-alt/control-center:$TAG" --build-arg TARGET_BIN=cmd/control-center .
docker build -q -f executors/content-fetcher/Dockerfile -t "kubetest-alt/content-fetcher:$TAG" .
for img in operator apiserver control-center content-fetcher; do
  kind load docker-image "kubetest-alt/$img:$TAG" --name "$CLUSTER"
done

say "namespaces, MinIO and Postgres"
for ns in "$NS" "$DEMO"; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  # Test pods upload logs and artifacts themselves: every namespace that
  # runs tests needs the storage credentials (make dev-namespace NS=…).
  kubectl -n "$ns" create secret generic s3-creds \
    --from-literal=AWS_ACCESS_KEY_ID=minioadmin --from-literal=AWS_SECRET_ACCESS_KEY=minioadmin \
    --dry-run=client -o yaml | kubectl apply -f -
done
kubectl -n "$NS" apply -f - <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata: {name: minio}
spec:
  selector: {matchLabels: {app: minio}}
  template:
    metadata: {labels: {app: minio}}
    spec:
      containers:
        - name: minio
          image: cgr.dev/chainguard/minio@sha256:a05a4497e8dce3cb7a7a1bf1872ba5d30ea988f1e8c22c9e0920503761c4b5f1
          args: ["server", "/data", "--console-address=:9001"]
          env:
            - {name: MINIO_ROOT_USER, value: minioadmin}
            - {name: MINIO_ROOT_PASSWORD, value: minioadmin}
          ports: [{containerPort: 9000}, {containerPort: 9001}]
          readinessProbe: {tcpSocket: {port: 9000}, periodSeconds: 2}
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: minio}
spec:
  selector: {app: minio}
  ports: [{name: s3, port: 9000}, {name: console, port: 9001}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: postgres}
spec:
  selector: {matchLabels: {app: postgres}}
  template:
    metadata: {labels: {app: postgres}}
    spec:
      containers:
        - name: postgres
          image: postgres:17-alpine
          env:
            - {name: POSTGRES_USER, value: kubetest}
            - {name: POSTGRES_PASSWORD, value: kubetest}
            - {name: POSTGRES_DB, value: kubetest}
          ports: [{containerPort: 5432}]
          readinessProbe: {exec: {command: ["pg_isready", "-U", "kubetest"]}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: postgres}
spec:
  selector: {app: postgres}
  ports: [{port: 5432}]
YAML
kubectl -n "$NS" rollout status deploy/minio --timeout=180s
kubectl -n "$NS" rollout status deploy/postgres --timeout=180s
kubectl -n "$NS" delete job minio-mkbucket --ignore-not-found >/dev/null
kubectl -n "$NS" apply -f - <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: {name: minio-mkbucket}
spec:
  backoffLimit: 5
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: cgr.dev/chainguard/minio-client@sha256:487ea889cfb126aeb8d51405dd8d2f8426da33734a8369338814c4eebe0179e6
          args: ["mb", "--ignore-existing", "local/kubetest-artifacts"]
          env:
            - {name: MC_HOST_local, value: "http://minioadmin:minioadmin@minio:9000"}
            - {name: MC_CONFIG_DIR, value: /tmp/mc}
          volumeMounts: [{name: tmp, mountPath: /tmp}]
      volumes: [{name: tmp, emptyDir: {}}]
YAML
kubectl -n "$NS" wait --for=condition=complete job/minio-mkbucket --timeout=180s

say "kubetest (chart with Control Center)"
helm upgrade --install kt charts/kubetest-alt -n "$NS" -f hack/dev-values.yaml --wait --timeout 5m
# Same image tag every time: restart so the pods run what was just built.
kubectl -n "$NS" rollout restart deploy/kt-kubetest-alt-operator deploy/kt-kubetest-alt-apiserver \
  deploy/kt-kubetest-alt-control-center >/dev/null
for d in operator apiserver control-center; do
  kubectl -n "$NS" rollout status "deploy/kt-kubetest-alt-$d" --timeout=180s
done

say "tool catalog and sample Tests in namespace $DEMO"
# Right after the restart the operator's admission webhook may not listen
# yet ("connection refused") although the Deployment is ready: retry.
for i in $(seq 1 24); do
  if kubectl -n "$DEMO" apply -f config/templates/ >/dev/null 2>&1 &&
     kubectl -n "$DEMO" apply -k config/samples/tools/ >/dev/null 2>&1; then
    break
  fi
  [ "$i" = 24 ] && { kubectl -n "$DEMO" apply -k config/samples/tools/; exit 1; }
  sleep 5
done
kubectl -n "$DEMO" get tests

cat <<TXT

kubetest is up on kind-$CLUSTER.

  Control Center   make dev-ui      → http://localhost:8090  (you are dev@local, admin)
  Start a run      kubectl create -n $DEMO -f - <<< '{"apiVersion":"tests.kubetest.io/v1alpha1","kind":"TestRun","metadata":{"generateName":"sample-k6-"},"spec":{"testRef":"sample-k6"}}'
  CLI              make install-cli && kubectl kubetest run sample-k6 -n $DEMO --follow
  Your own Tests   kubectl apply -n $DEMO -f my-test.yaml   (another namespace: make dev-namespace NS=…)
  After a change   make dev-reload  (rebuild + restart the platform)
  Tear down        make dev-down

The samples fetch their projects from GitHub (this repository), so the
cluster needs internet access; the first run of a tool also pulls its image.
TXT
