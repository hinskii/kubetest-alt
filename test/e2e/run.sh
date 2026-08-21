#!/usr/bin/env bash
# Kind e2e driver — builds all platform images, loads them into the
# kind node, helm-installs the chart, port-forwards operator /metrics
# + apiserver, then invokes `go test -tags=e2e` for the 5 plan
# scenarios. Trap-on-exit tears down.
#
# Called from make test-e2e AND from .github/workflows/test-e2e.yml.
# Wall-clock budget from the plan: ≤15 min. If we go over on the CI
# run we split PR-gate/nightly per plan (report the timings).

set -euo pipefail

KIND_CLUSTER="${KIND_CLUSTER:-kubetest-alt-e2e}"
RELEASE_NS="kubetest-alt"
CHART_DIR="charts/kubetest-alt"
IMAGE_TAG="e2e-local"

# All platform images the chart references. Built + `kind load`ed so
# ImagePullPolicy=Never works inside the cluster.
IMAGES=(
  "kubetest-alt/operator:${IMAGE_TAG}"
  "kubetest-alt/apiserver:${IMAGE_TAG}"
  "kubetest-alt/content-fetcher:${IMAGE_TAG}"
)

log() { echo "[e2e] $(date +%H:%M:%S) $*" >&2; }

# Timings recorded per phase for the report.
declare -A PHASE_START
phase_start() { PHASE_START["$1"]=$(date +%s); log "PHASE_START $1"; }
phase_end()   {
  local phase="$1"
  local start="${PHASE_START[$phase]}"
  local now=$(date +%s)
  log "PHASE_TIMING phase=$phase duration_seconds=$((now - start))"
}

cleanup() {
  log "cleanup — deleting kind cluster $KIND_CLUSTER"
  # port-forwards die with this process
  kind delete cluster --name "$KIND_CLUSTER" || true
  # Kill any lingering port-forward we spawned
  jobs -p | xargs -r kill 2>/dev/null || true
}
trap cleanup EXIT

phase_start "kind_create"
if ! kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER}\$"; then
  kind create cluster --name "$KIND_CLUSTER" --wait 60s
else
  log "kind cluster $KIND_CLUSTER already exists; reusing"
fi
phase_end "kind_create"

phase_start "docker_build"
docker build -f Dockerfile           -t "kubetest-alt/operator:${IMAGE_TAG}"        --build-arg TARGET_BIN=cmd/operator .
docker build -f Dockerfile           -t "kubetest-alt/apiserver:${IMAGE_TAG}"       --build-arg TARGET_BIN=cmd/apiserver .
docker build -f executors/content-fetcher/Dockerfile -t "kubetest-alt/content-fetcher:${IMAGE_TAG}" executors/content-fetcher
phase_end "docker_build"

phase_start "kind_load"
for img in "${IMAGES[@]}"; do
  kind load docker-image "$img" --name "$KIND_CLUSTER"
done
phase_end "kind_load"

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
  --wait --timeout=3m
# Health check for both deployments
kubectl -n "$RELEASE_NS" rollout status deploy/kt-kubetest-alt-operator  --timeout=120s
kubectl -n "$RELEASE_NS" rollout status deploy/kt-kubetest-alt-apiserver --timeout=120s
phase_end "helm_install"

# Port-forward for metrics + apiserver — background jobs die on trap.
phase_start "portforward"
# Operator metrics: --metrics-secure=true by default; scenarios only
# need presence + a counter — hit the pod's :8443 via kubectl proxy
# would need auth. Simpler for e2e: relax secure via values override
# (already flagged) OR bind :8080 + insecure.
# We flip metrics.secure=false for the e2e install via a small upgrade
# so the scrape works without a TokenReview.
helm upgrade kt "$CHART_DIR" --namespace "$RELEASE_NS" --reuse-values \
  --set operator.metrics.bindAddress=":8080" \
  --set operator.metrics.secure=false --wait --timeout=60s
kubectl -n "$RELEASE_NS" rollout status deploy/kt-kubetest-alt-operator --timeout=120s

kubectl -n "$RELEASE_NS" port-forward svc/kt-kubetest-alt-apiserver 18080:8080 >/dev/null 2>&1 &
kubectl -n "$RELEASE_NS" port-forward deploy/kt-kubetest-alt-operator  18081:8080 >/dev/null 2>&1 &
sleep 3
phase_end "portforward"

export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
export APISERVER_URL="http://127.0.0.1:18080"
export METRICS_APISERVER_URL="http://127.0.0.1:18080/metrics"
export METRICS_OPERATOR_URL="http://127.0.0.1:18081/metrics"

phase_start "go_test"
go test -tags=e2e -count=1 -v -timeout=15m ./test/e2e/... 2>&1
phase_end "go_test"

log "all scenarios passed"
