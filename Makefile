# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: openapi
openapi: ## Regenerate openapi/openapi.json from internal/apiserver.OpenAPISpec.
	go run ./cmd/gen-openapi openapi/openapi.json

.PHONY: openapi-check
openapi-check: ## Zero-diff gate: regenerate the spec into a tmpfile and diff against committed.
	@tmp=$$(mktemp); \
	go run ./cmd/gen-openapi $$tmp; \
	if ! diff -q $$tmp openapi/openapi.json >/dev/null; then \
		echo "openapi-check: openapi/openapi.json is out of date — run 'make openapi'"; \
		diff -u openapi/openapi.json $$tmp | head -40; \
		rm -f $$tmp; \
		exit 1; \
	fi; \
	rm -f $$tmp; \
	echo "openapi-check: openapi/openapi.json is in sync with code"

.PHONY: test
test: manifests generate fmt vet openapi-check setup-envtest ## Run tests with -race (no Docker).
	KUBEBUILDER_ASSETS="$$("$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		go test -race $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: test-integration
test-integration: manifests generate fmt vet setup-envtest ## Run integration tests (requires Docker: testcontainers-go).
	KUBEBUILDER_ASSETS="$$("$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		go test -tags integration -race -timeout 5m $$(go list ./... | grep -v /e2e) -coverprofile cover.integration.out

# COVERAGE_PKGS_INTEGRATION covers packages whose non-trivial code paths
# only exercise under the `integration` build tag (real Postgres for
# internal/store). Same 80 floor the plan sets for core-logic packages —
# split from the default gate so `make test` stays Docker-free.
COVERAGE_PKGS_INTEGRATION ?= internal/store:80

.PHONY: test-coverage-integration
test-coverage-integration: test-integration ## Enforce COVERAGE_PKGS_INTEGRATION floors on integration test coverage.
	@if [ -z "$(COVERAGE_PKGS_INTEGRATION)" ]; then \
		echo "test-coverage-integration: COVERAGE_PKGS_INTEGRATION is empty."; \
		exit 0; \
	fi; \
	fail=0; \
	for entry in $(COVERAGE_PKGS_INTEGRATION); do \
		pkg=$${entry%%:*}; \
		min=$${entry##*:}; \
		if [ "$$min" = "$$entry" ]; then min="$(COVERAGE_MIN)"; fi; \
		pct=$$(awk -v p="$$pkg" ' \
			NR == 1 { next } \
			$$1 ~ p { total += $$2; if ($$3 > 0) covered += $$2 } \
			END { if (total > 0) printf "%.1f", 100.0 * covered / total; else print "NA" }' \
			cover.integration.out); \
		if [ "$$pct" = "NA" ]; then \
			echo "test-coverage-integration: no coverage data for $$pkg"; fail=1; continue; \
		fi; \
		awk -v pct="$$pct" -v min="$$min" 'BEGIN { exit !(pct+0 >= min+0) }' \
			&& echo "test-coverage-integration: $$pkg $$pct%% >= $$min%%" \
			|| { echo "test-coverage-integration: $$pkg $$pct%% < $$min%%"; fail=1; }; \
	done; \
	exit $$fail

# COVERAGE_PKGS lists packages that must meet the coverage bar. Entries take
# the form `path` (uses COVERAGE_MIN as the floor) or `path:min` (per-package
# override). Compiler is the pure-function core so gets 90; pkg/executor has
# some inherent fs-error branches that are impractical to trigger without
# fs mocks — 80 matches the plan/README.md convention for core-logic packages.
#
# internal/store is enforced under `make test-coverage-integration` at 80
# (plan-mandated core-logic bar), NOT here — postgres.go / migrations.go
# require the `integration` build tag + real Postgres via testcontainers-go,
# which the default `make test` deliberately excludes to stay Docker-free
# (§step-09 acceptance). Splitting keeps two invariants intact: default gate
# has no Docker dep, and store still meets the same 80% bar the plan sets.
COVERAGE_PKGS ?= internal/compiler:90 pkg/executor:80 internal/scraper:85 internal/logstream:85 internal/apiserver:80
COVERAGE_MIN ?= 80

.PHONY: test-coverage
test-coverage: test ## Enforce coverage floors on COVERAGE_PKGS (supports pkg:min syntax).
	@if [ -z "$(COVERAGE_PKGS)" ]; then \
		echo "test-coverage: COVERAGE_PKGS is empty; nothing to enforce yet (add packages as they land)."; \
		exit 0; \
	fi; \
	fail=0; \
	for entry in $(COVERAGE_PKGS); do \
		pkg=$${entry%%:*}; \
		min=$${entry##*:}; \
		if [ "$$min" = "$$entry" ]; then min="$(COVERAGE_MIN)"; fi; \
		pct=$$(awk -v p="$$pkg" ' \
			NR == 1 { next } \
			$$1 ~ p { total += $$2; if ($$3 > 0) covered += $$2 } \
			END { if (total > 0) printf "%.1f", 100.0 * covered / total; else print "NA" }' \
			cover.out); \
		if [ "$$pct" = "NA" ]; then \
			echo "test-coverage: no coverage data for $$pkg"; fail=1; continue; \
		fi; \
		awk -v pct="$$pct" -v min="$$min" 'BEGIN { exit !(pct+0 >= min+0) }' \
			&& echo "test-coverage: $$pkg $$pct%% >= $$min%%" \
			|| { echo "test-coverage: $$pkg $$pct%% < $$min%%"; fail=1; }; \
	done; \
	exit $$fail

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# kubectl kuberc is disabled by default for test isolation; enable with:
# - KUBECTL_KUBERC=true
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= kubetest-alt-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager ./cmd/operator

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/operator

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name kubetest-alt-builder
	$(CONTAINER_TOOL) buildx use kubetest-alt-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm kubetest-alt-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.21.0

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.12.2
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
