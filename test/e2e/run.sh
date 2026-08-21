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

declare -A PHASE_START
phase_start() { PHASE_START["$1"]=$(date +%s); log "PHASE_START $1"; }
phase_end() {
  local phase="$1"
  local start="${PHASE_START[$phase]}"
  local now=$(date +%s)
  log "PHASE_TIMING phase=$phase duration_seconds=$((now - start))"
}

cleanup() {
  log "cleanup — deleting kind cluster $KIND_CLUSTER"
  kind delete cluster --name "$KIND_CLUSTER" || true
  jobs -p | xargs -r kill 2>/dev/null || true
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

phase_start "minio_deploy"
# The operator's ResultReader is a no-op without --minio-endpoint —
# every run would then be classified MissingResult / phase=error.
# Deploy a single-pod in-memory MinIO in the release namespace, then
# mirror the creds Secret into the workload namespace so the wrapper's
# envFrom picks them up. Bucket is created by a small Job that runs
# `mc mb` once MinIO is Ready. No PVC — kind cluster is ephemeral.
kubectl create namespace "$RELEASE_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace kubetest-e2e --dry-run=client -o yaml | kubectl apply -f -

# Creds Secret (release namespace = operator's; also mirrored into workload namespace below).
kubectl -n "$RELEASE_NS" create secret generic minio-creds \
  --from-literal=AWS_ACCESS_KEY_ID=minioadmin \
  --from-literal=AWS_SECRET_ACCESS_KEY=minioadmin \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n kubetest-e2e create secret generic minio-creds \
  --from-literal=AWS_ACCESS_KEY_ID=minioadmin \
  --from-literal=AWS_SECRET_ACCESS_KEY=minioadmin \
  --dry-run=client -o yaml | kubectl apply -f -

# MinIO Deployment + Service.
cat <<'EOF' | kubectl -n "$RELEASE_NS" apply -f -
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
          image: minio/minio:RELEASE.2025-04-08T15-41-24Z
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
kubectl -n "$RELEASE_NS" rollout status deploy/minio --timeout=120s

# Bucket-create Job — one-shot `mc mb`. Idempotent with `--ignore-existing`.
cat <<'EOF' | kubectl -n "$RELEASE_NS" apply -f -
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
          image: minio/mc:RELEASE.2025-04-03T17-07-56Z
          command:
            - sh
            - -c
            - |
              set -eux
              mc alias set local http://minio:9000 minioadmin minioadmin
              mc mb --ignore-existing local/kubetest-artifacts
              mc mb --ignore-existing local/kubetest-logs
EOF
kubectl -n "$RELEASE_NS" wait --for=condition=complete job/minio-mkbucket --timeout=120s
phase_end "minio_deploy"

phase_start "helm_install"
helm upgrade --install kt "$CHART_DIR" \
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
  --set "minio.endpoint=minio.${RELEASE_NS}.svc:9000" \
  --set minio.secretName=minio-creds \
  --set minio.bucket=kubetest-artifacts \
  --set "apiserver.extraArgs={--namespace=kubetest-e2e}" \
  --wait --timeout=5m
kubectl -n "$RELEASE_NS" get all
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

phase_start "go_test"
go test -tags=e2e -count=1 -v -timeout=15m ./test/e2e/...
phase_end "go_test"

log "all scenarios passed"
