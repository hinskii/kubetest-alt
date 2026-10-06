#!/usr/bin/env bash
# Kind e2e driver — builds all platform images, loads them into the
# kind node, helm-installs the chart, port-forwards operator /metrics
# + apiserver, then invokes `go test -tags=e2e` for the 5 plan
# scenarios. Trap-on-exit tears down.
#
# Called from make test-e2e AND from .github/workflows/test-e2e.yml.
# Wall-clock budget from the plan: ≤15 min. If we go over on the CI
# run we split PR-gate/nightly per plan (report the timings).
#
# Diagnostic mode: `set -x` on so every command echoes to stderr.
# The 1e1cff1 CI run reported success in 65s (impossible for a real
# kind + 3x docker build + helm install + 5 scenarios). We want the
# artifact log to make the WHY unambiguous — silent short-circuits
# are the failure mode we're guarding against.

set -euxo pipefail

# E2E_SUITE: e2e (default — platform scenarios), catalog (every catalog
# template run for real, see test/catalog), or all.
# CATALOG_TOOLS: comma-separated subset of test/catalog/cases (default all).
E2E_SUITE="${E2E_SUITE:-e2e}"
# E2E_STORAGE: s3 (MinIO, default) or gcs (fake-gcs-server emulator).
E2E_STORAGE="${E2E_STORAGE:-s3}"
FAKE_GCS_IMAGE="fsouza/fake-gcs-server:1.56.1"
CURL_IMAGE="curlimages/curl:8.16.0"
CATALOG_TOOLS="${CATALOG_TOOLS:-all}"
CATALOG_PARALLEL="${CATALOG_PARALLEL:-4}"

KIND_CLUSTER="${KIND_CLUSTER:-kubetest-alt-e2e}"
RELEASE_NS="kubetest-alt"
CHART_DIR="charts/kubetest-alt"
IMAGE_TAG="e2e-local"

IMAGES=(
  "kubetest-alt/operator:${IMAGE_TAG}"
  "kubetest-alt/apiserver:${IMAGE_TAG}"
  "kubetest-alt/content-fetcher:${IMAGE_TAG}"
)

log() { echo "[e2e] $(date +%H:%M:%S) $*" >&2; }

# Plain variables instead of `declare -A` so the script also runs on
# macOS's bash 3.2 for local development.
phase_start() { printf -v "PHASE_START_$1" '%s' "$(date +%s)"; log "PHASE_START $1"; }
phase_end() {
  local phase="$1"
  local var="PHASE_START_$phase"
  local start="${!var}"
  local now=$(date +%s)
  log "PHASE_TIMING phase=$phase duration_seconds=$((now - start))"
}

cleanup() {
  jobs -p | xargs kill 2>/dev/null || true
  if [ "${E2E_KEEP_CLUSTER:-}" = "1" ]; then
    log "E2E_KEEP_CLUSTER=1 — leaving kind cluster $KIND_CLUSTER running"
    return
  fi
  log "cleanup — deleting kind cluster $KIND_CLUSTER"
  kind delete cluster --name "$KIND_CLUSTER" || true
}
trap cleanup EXIT

# Pre-flight assertions — surface missing tooling with a loud message
# rather than the runner's default cryptic "command not found".
log "=== pre-flight ==="
docker version --format 'server={{.Server.Version}} client={{.Client.Version}}'
kind version
helm version --short
kubectl version --client
go version

phase_start "kind_create"
if ! kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER}\$"; then
  kind create cluster --name "$KIND_CLUSTER" --wait 120s
else
  log "kind cluster $KIND_CLUSTER already exists; reusing"
fi
kubectl cluster-info --context "kind-${KIND_CLUSTER}"
kubectl get nodes
phase_end "kind_create"

phase_start "docker_build"
docker build -f Dockerfile -t "kubetest-alt/operator:${IMAGE_TAG}"   --build-arg TARGET_BIN=cmd/operator  .
docker build -f Dockerfile -t "kubetest-alt/apiserver:${IMAGE_TAG}"  --build-arg TARGET_BIN=cmd/apiserver .
# Content-fetcher Dockerfile COPYs go.mod, go.sum, api/, pkg/, cmd/entry/
# — all repo-root paths. Build context MUST be repo root; the previous
# `executors/content-fetcher` context left every COPY failing with
# "/api: not found".
docker build -f executors/content-fetcher/Dockerfile \
             -t "kubetest-alt/content-fetcher:${IMAGE_TAG}" .
# Assert each image landed — earlier runs mysteriously passed in 65s
# which is impossible if the builds actually ran; a missing image at
# this point should now shout, not silently proceed.
for img in "${IMAGES[@]}"; do
  docker image inspect "$img" >/dev/null || { log "IMAGE MISSING: $img"; exit 1; }
done
phase_end "docker_build"

phase_start "kind_load"
for img in "${IMAGES[@]}"; do
  kind load docker-image "$img" --name "$KIND_CLUSTER"
done
phase_end "kind_load"

phase_start "storage_deploy"
# Object storage for logs, artifacts and result.json. Without it every run
# would be judged from pod state only. E2E_STORAGE picks the backend:
#   s3  — MinIO (an S3-compatible store) in the release namespace;
#   gcs — fake-gcs-server (GCS emulator), exercising the native GCS backend.
# No PVCs — the kind cluster is ephemeral.
kubectl create namespace "$RELEASE_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace kubetest-e2e --dry-run=client -o yaml | kubectl apply -f -

if [ "$E2E_STORAGE" = "s3" ]; then
  # S3 credentials: the operator/API server read them in the release
  # namespace; the wrapper gets the same-named Secret via envFrom in every
  # namespace that runs tests.
  for ns in "$RELEASE_NS" kubetest-e2e; do
    kubectl -n "$ns" create secret generic s3-creds \
      --from-literal=AWS_ACCESS_KEY_ID=minioadmin \
      --from-literal=AWS_SECRET_ACCESS_KEY=minioadmin \
      --dry-run=client -o yaml | kubectl apply -f -
  done
# MinIO Deployment + Service.
#
# Images: minio/minio and minio/mc were pulled from Docker Hub (and Quay)
# upstream in 2026 — every tag 404s, which left this script timing out on
# `rollout status` before any scenario ran. Chainguard publishes both; the
# free tier only serves :latest, so we pin by digest for reproducibility.
# Both are shell-less (entrypoint = the binary), hence no `sh -c` below.
MINIO_IMAGE="cgr.dev/chainguard/minio@sha256:a05a4497e8dce3cb7a7a1bf1872ba5d30ea988f1e8c22c9e0920503761c4b5f1"
MC_IMAGE="cgr.dev/chainguard/minio-client@sha256:487ea889cfb126aeb8d51405dd8d2f8426da33734a8369338814c4eebe0179e6"
cat <<EOF | kubectl -n "$RELEASE_NS" apply -f -
apiVersion: apps/v1
kind: Deployment
metadata: { name: minio }
spec:
  replicas: 1
  selector: { matchLabels: { app: minio } }
  template:
    metadata: { labels: { app: minio } }
    spec:
      containers:
        - name: minio
          image: ${MINIO_IMAGE}
          args: ["server", "/data", "--console-address=:9001"]
          env:
            - { name: MINIO_ROOT_USER, value: minioadmin }
            - { name: MINIO_ROOT_PASSWORD, value: minioadmin }
          ports:
            - { containerPort: 9000, name: s3 }
            - { containerPort: 9001, name: console }
          volumeMounts:
            - { name: data, mountPath: /data }
          readinessProbe:
            tcpSocket: { port: 9000 }
            periodSeconds: 2
            timeoutSeconds: 2
      volumes:
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata: { name: minio }
spec:
  selector: { app: minio }
  ports:
    - { name: s3, port: 9000, targetPort: 9000 }
EOF
if ! kubectl -n "$RELEASE_NS" rollout status deploy/minio --timeout=120s; then
  log "::error::MinIO did not become Ready — dumping pod state"
  kubectl -n "$RELEASE_NS" describe pods -l app=minio || true
  kubectl -n "$RELEASE_NS" get events --sort-by=.lastTimestamp | tail -30 || true
  exit 1
fi

# Bucket-create Job — one-shot `mc mb`. Idempotent with `--ignore-existing`.
# MC_HOST_local carries endpoint + creds, so no `mc alias set` (and no
# shell) is needed; MC_CONFIG_DIR points at a writable emptyDir because
# the image runs as non-root.
cat <<EOF | kubectl -n "$RELEASE_NS" apply -f -
apiVersion: batch/v1
kind: Job
metadata: { name: minio-mkbucket }
spec:
  backoffLimit: 3
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: ${MC_IMAGE}
          args: ["mb", "--ignore-existing", "local/kubetest-artifacts"]
          env:
            - { name: MC_HOST_local, value: "http://minioadmin:minioadmin@minio:9000" }
            - { name: MC_CONFIG_DIR, value: /tmp/mc }
          volumeMounts:
            - { name: tmp, mountPath: /tmp }
      volumes:
        - name: tmp
          emptyDir: {}
EOF
if ! kubectl -n "$RELEASE_NS" wait --for=condition=complete job/minio-mkbucket --timeout=120s; then
  log "::error::bucket-create Job did not complete"
  kubectl -n "$RELEASE_NS" logs job/minio-mkbucket --all-containers=true || true
  exit 1
fi
  STORAGE_VALUES=(
    --set storage.type=s3
    --set storage.bucket=kubetest-artifacts
    --set "storage.s3.endpoint=minio.${RELEASE_NS}.svc:9000"
    --set storage.s3.useSSL=false
    --set storage.s3.secretName=s3-creds
  )
else
  # GCS emulator. Memory backend; bucket created through the JSON API by a
  # one-shot Job. Test pods in other namespaces reach it by FQDN.
  cat <<EOF | kubectl -n "$RELEASE_NS" apply -f -
apiVersion: apps/v1
kind: Deployment
metadata: { name: fake-gcs }
spec:
  replicas: 1
  selector: { matchLabels: { app: fake-gcs } }
  template:
    metadata: { labels: { app: fake-gcs } }
    spec:
      # Kubernetes injects FAKE_GCS_PORT=tcp://<ip>:4443 for this Service,
      # which fake-gcs-server reads as its own -port setting and dies on.
      enableServiceLinks: false
      containers:
        - name: fake-gcs
          image: ${FAKE_GCS_IMAGE}
          args: ["-scheme", "http", "-port", "4443", "-backend", "memory",
                 "-public-host", "fake-gcs.${RELEASE_NS}.svc:4443"]
          ports: [{ containerPort: 4443 }]
          readinessProbe: { tcpSocket: { port: 4443 }, periodSeconds: 2 }
---
apiVersion: v1
kind: Service
metadata: { name: fake-gcs }
spec:
  selector: { app: fake-gcs }
  ports: [{ port: 4443, targetPort: 4443 }]
EOF
  if ! kubectl -n "$RELEASE_NS" rollout status deploy/fake-gcs --timeout=120s; then
    log "::error::fake-gcs did not become Ready"
    kubectl -n "$RELEASE_NS" describe pods -l app=fake-gcs || true
    kubectl -n "$RELEASE_NS" logs deploy/fake-gcs || true
    exit 1
  fi
  cat <<EOF | kubectl -n "$RELEASE_NS" apply -f -
apiVersion: batch/v1
kind: Job
metadata: { name: fake-gcs-mkbucket }
spec:
  backoffLimit: 10
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: curl
          image: ${CURL_IMAGE}
          args: ["-fsS", "-X", "POST", "-H", "Content-Type: application/json",
                 "-d", "{\"name\":\"kubetest-artifacts\"}",
                 "http://fake-gcs:4443/storage/v1/b?project=e2e"]
EOF
  if ! kubectl -n "$RELEASE_NS" wait --for=condition=complete job/fake-gcs-mkbucket --timeout=120s; then
    log "::error::fake-gcs bucket Job did not complete"
    kubectl -n "$RELEASE_NS" describe pods -l app=fake-gcs || true
    kubectl -n "$RELEASE_NS" logs job/fake-gcs-mkbucket --all-containers=true || true
    exit 1
  fi
  STORAGE_VALUES=(
    --set storage.type=gcs
    --set storage.bucket=kubetest-artifacts
    --set "storage.gcs.endpoint=http://fake-gcs.${RELEASE_NS}.svc:4443"
  )
fi
phase_end "storage_deploy"

phase_start "helm_install"
if ! helm upgrade --install kt "$CHART_DIR" \
  --namespace "$RELEASE_NS" --create-namespace \
  --set images.registry="" \
  --set images.pullPolicy=Never \
  --set images.operator.repository=kubetest-alt/operator \
  --set images.operator.tag="${IMAGE_TAG}" \
  --set images.apiserver.repository=kubetest-alt/apiserver \
  --set images.apiserver.tag="${IMAGE_TAG}" \
  --set images.contentFetcher.repository=kubetest-alt/content-fetcher \
  --set images.contentFetcher.tag="${IMAGE_TAG}" \
  --set operator.metrics.bindAddress=":8080" \
  --set operator.metrics.secure=false \
  "${STORAGE_VALUES[@]}" \
  --wait --timeout=5m; then
  log "::error::helm install failed — dumping cluster state for diagnosis"
  kubectl -n "$RELEASE_NS" get pods,deploy,jobs -o wide || true
  kubectl -n "$RELEASE_NS" describe pods || true
  for pod in $(kubectl -n "$RELEASE_NS" get pods -o name 2>/dev/null); do
    log "--- logs for $pod ---"
    kubectl -n "$RELEASE_NS" logs "$pod" --all-containers=true --tail=200 || true
  done
  exit 1
fi
kubectl -n "$RELEASE_NS" get all
# Same image tag on every run: when a kept cluster is reused, helm sees no
# change and the old pods would keep running the previous build.
kubectl -n "$RELEASE_NS" rollout restart deploy/kt-kubetest-alt-operator deploy/kt-kubetest-alt-apiserver
kubectl -n "$RELEASE_NS" rollout status deploy/kt-kubetest-alt-operator  --timeout=180s
kubectl -n "$RELEASE_NS" rollout status deploy/kt-kubetest-alt-apiserver --timeout=180s

# Install TestTemplates in the workload namespace. Scenario 2 (jmeter)
# uses `use: jmeter` — resolver looks up the template in the Test's own
# namespace, so templates must live there. Direct -f apply of every YAML
# under config/templates/ is simpler than a kustomize -k invocation
# (kustomize's default LoadRestrictor forbade the samples' ../../ path
# on the previous CI attempt).
kubectl -n kubetest-e2e apply -f config/templates/
phase_end "helm_install"

phase_start "portforward"
kubectl -n "$RELEASE_NS" port-forward svc/kt-kubetest-alt-apiserver 18080:8080 >/dev/null 2>&1 &
kubectl -n "$RELEASE_NS" port-forward deploy/kt-kubetest-alt-operator 18081:8080 >/dev/null 2>&1 &
sleep 5
# Sanity — did port-forwards actually attach? Curl each one, non-fatal
# but visibly logged so scenario timeouts are diagnosable.
curl -sSf "http://127.0.0.1:18080/healthz" || log "WARN apiserver /healthz not reachable via port-forward"
curl -sSf "http://127.0.0.1:18081/metrics" || log "WARN operator /metrics not reachable via port-forward"
phase_end "portforward"

export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
export APISERVER_URL="http://127.0.0.1:18080"
export METRICS_APISERVER_URL="http://127.0.0.1:18080/metrics"
export METRICS_OPERATOR_URL="http://127.0.0.1:18081/metrics"

if [ "$E2E_SUITE" = "e2e" ] || [ "$E2E_SUITE" = "all" ]; then
  phase_start "go_test"
  go test -tags=e2e -count=1 -v -timeout=15m ./test/e2e/...
  phase_end "go_test"
  log "all scenarios passed"
fi

if [ "$E2E_SUITE" = "catalog" ] || [ "$E2E_SUITE" = "all" ]; then
  wants() { [ "$CATALOG_TOOLS" = "all" ] || [[ ",$CATALOG_TOOLS," == *",$1,"* ]]; }

  phase_start "catalog_images"
  # Platform-built tool images (CLAUDE.md §3 exceptions): build from this
  # tree so the catalog run tests the current Dockerfiles, and load them
  # under the exact names the templates reference.
  for tool in gatling soapui kubepug; do
    wants "$tool" || continue
    img="$(grep -h 'image:' "config/templates/${tool}.yaml" | head -1 | awk '{print $2}')"
    docker build -t "$img" "executors/${tool}"
    kind load docker-image "$img" --name "$KIND_CLUSTER"
  done
  phase_end "catalog_images"

  phase_start "catalog_target"
  # Own namespace: the platform e2e deletes kubetest-e2e when it finishes.
  CATALOG_NS=kubetest-catalog
  kubectl create namespace "$CATALOG_NS" --dry-run=client -o yaml | kubectl apply -f -
  if [ "$E2E_STORAGE" = "s3" ]; then
    kubectl -n "$CATALOG_NS" create secret generic s3-creds \
      --from-literal=AWS_ACCESS_KEY_ID=minioadmin \
      --from-literal=AWS_SECRET_ACCESS_KEY=minioadmin \
      --dry-run=client -o yaml | kubectl apply -f -
  fi
  kubectl -n "$CATALOG_NS" apply -f config/templates/
  # Target every case talks to: "/" → 200 with a small page, anything
  # else → 404 (python http.server), so each tool sees passes AND failures.
  cat <<'EOF' | kubectl -n "$CATALOG_NS" apply -f -
apiVersion: v1
kind: ConfigMap
metadata: { name: target-www }
data:
  index.html: |
    <!doctype html>
    <html lang="en"><head><title>kubetest target</title></head>
    <body><h1>ok</h1></body></html>
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: target }
spec:
  replicas: 1
  selector: { matchLabels: { app: target } }
  template:
    metadata: { labels: { app: target } }
    spec:
      containers:
        - name: http
          image: python:3.13-alpine
          command: ["python", "-m", "http.server", "8000", "--directory", "/www"]
          ports: [{ containerPort: 8000 }]
          readinessProbe: { tcpSocket: { port: 8000 }, periodSeconds: 2 }
          volumeMounts: [{ name: www, mountPath: /www }]
      volumes: [{ name: www, configMap: { name: target-www } }]
---
apiVersion: v1
kind: Service
metadata: { name: target }
spec:
  selector: { app: target }
  ports: [{ port: 8000, targetPort: 8000 }]
EOF
  kubectl -n "$CATALOG_NS" rollout status deploy/target --timeout=120s
  phase_end "catalog_target"

  phase_start "catalog_test"
  CATALOG_TOOLS="$CATALOG_TOOLS" CATALOG_GIT_REVISION="${CATALOG_GIT_REVISION:-}" go test -tags=catalog -count=1 -v -timeout=60m \
    -parallel "$CATALOG_PARALLEL" ./test/catalog/...
  phase_end "catalog_test"
  log "catalog passed: $CATALOG_TOOLS"
fi
