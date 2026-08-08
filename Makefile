# Image URLs used by the container build and push targets.
IMG ?= controller:latest
WORKER_IMG ?= worker:latest
INJECTOR_IMG ?= injector:latest

# Get the currently used Go install path (GOPATH/bin unless GOBIN is set).
ifeq (,$(shell go env GOBIN))
GOBIN := $(shell go env GOPATH)/bin
else
GOBIN := $(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool used to build images. The targets
# are tested with Docker, but another compatible tool (for example, Podman)
# can be supplied by callers.
CONTAINER_TOOL ?= docker

# Run recipes with bash and fail when a command or command in a pipeline fails.
SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

.DEFAULT_GOAL := help

# Generation, formatting, vetting, and compilation must not race under make -j.
.NOTPARALLEL: build run run-dashboard test test-e2e

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate RBAC roles and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate DeepCopy method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="boilerplate.go.txt" paths="./..."

.PHONY: gen-protoc
gen-protoc: ## Regenerate Go RPC stubs from rpc/*.proto (requires protoc).
	./generate.sh

.PHONY: gen-rpc
gen-rpc: gen-protoc ## Regenerate Go and TypeScript RPC stubs (requires buf and frontend dependencies).
	buf generate

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run non-e2e Go tests with envtest and write cover.out.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

KIND_CLUSTER ?= gratefulagents-test-e2e
E2E_CLUSTER_MARKER = $(LOCALBIN)/.kind-cluster-$(KIND_CLUSTER)-created

.PHONY: setup-test-e2e
setup-test-e2e: | $(LOCALBIN) ## Create the Kind cluster used by e2e tests if necessary.
	@command -v "$(KIND)" >/dev/null 2>&1 || { echo "Kind is not installed. Install Kind before running e2e tests."; exit 1; }
	@if "$(KIND)" get clusters | grep -Fxq -- "$(KIND_CLUSTER)"; then \
		echo "Kind cluster '$(KIND_CLUSTER)' already exists. Reusing it without taking cleanup ownership."; \
		rm -f "$(E2E_CLUSTER_MARKER)"; \
	else \
		echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
		"$(KIND)" create cluster --name "$(KIND_CLUSTER)"; \
		touch "$(E2E_CLUSTER_MARKER)"; \
	fi

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e suite against Kind.
	@cleanup() { \
		status=$$?; \
		if [ -f "$(E2E_CLUSTER_MARKER)" ]; then \
			"$(KIND)" delete cluster --name "$(KIND_CLUSTER)"; \
			rm -f "$(E2E_CLUSTER_MARKER)"; \
		else \
			echo "Kind cluster '$(KIND_CLUSTER)' was not created by this workflow; leaving it running."; \
		fi; \
		exit $$status; \
	}; \
	trap cleanup EXIT; \
	KIND="$(KIND)" KIND_CLUSTER="$(KIND_CLUSTER)" go test -tags=e2e ./test/e2e/ -v -ginkgo.v

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Delete the Kind cluster only when this Makefile created it.
	@if [ -f "$(E2E_CLUSTER_MARKER)" ]; then \
		"$(KIND)" delete cluster --name "$(KIND_CLUSTER)"; \
		rm -f "$(E2E_CLUSTER_MARKER)"; \
	else \
		echo "Kind cluster '$(KIND_CLUSTER)' was not created by this workflow; leaving it running."; \
	fi

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply supported fixes.
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify the golangci-lint configuration.
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build the manager binary into bin/manager.
	go build -o bin/manager ./cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run the controller manager from the host.
	go run ./cmd/main.go

.PHONY: run-dashboard
run-dashboard: manifests generate fmt vet ## Run the controller manager with the dashboard on localhost:8090.
	go run ./cmd/main.go --enable-dashboard --dashboard-addr=localhost:8090

.PHONY: docker-build-all docker-build docker-build-worker docker-build-injector
docker-build-all: docker-build docker-build-worker docker-build-injector ## Build all project images.

docker-build: ## Build the controller image.
	$(CONTAINER_TOOL) build -t $(IMG) .

docker-build-worker: ## Build the worker image.
	$(CONTAINER_TOOL) build -t $(WORKER_IMG) -f Dockerfile.worker .

docker-build-injector: ## Build the injector image.
	$(CONTAINER_TOOL) build -t $(INJECTOR_IMG) -f Dockerfile.injector .

.PHONY: docker-push-all docker-push docker-push-worker docker-push-injector
docker-push-all: docker-push docker-push-worker docker-push-injector ## Push all project images.

docker-push: ## Push the controller image.
	$(CONTAINER_TOOL) push $(IMG)

docker-push-worker: ## Push the worker image.
	$(CONTAINER_TOOL) push $(WORKER_IMG)

docker-push-injector: ## Push the injector image.
	$(CONTAINER_TOOL) push $(INJECTOR_IMG)

PLATFORMS ?= linux/arm64,linux/amd64
.PHONY: docker-buildx
docker-buildx: ## Build and push the controller image for multiple platforms.
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag $(IMG) .

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated install manifest in dist/install.yaml.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=$(IMG)
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the cluster in the current kubeconfig.
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the cluster in the current kubeconfig.
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy the controller to the cluster in the current kubeconfig.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=$(IMG)
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove the controller from the cluster in the current kubeconfig.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Self-hosting

.PHONY: k3s-prereqs
k3s-prereqs: ## Install Debian/Ubuntu prerequisites for k3s management.
	./scripts/install-k3s-dependencies.sh

.PHONY: kind-install
kind-install: ## Install or update the main build in a local Kind cluster.
	./scripts/install-kind.sh

.PHONY: k3s-install
k3s-install: ## Install or update the latest release on a self-hosted k3s server.
	./scripts/install-k3s.sh

.PHONY: k3s-upgrade
k3s-upgrade: ## Fetch the latest published release and apply it to k3s.
	./scripts/install-k3s.sh

.PHONY: k3s-status
k3s-status: ## Show Kubernetes nodes, namespaces, and workloads.
	$(KUBECTL) get nodes
	$(KUBECTL) get namespaces
	$(KUBECTL) get deployments --all-namespaces

.PHONY: test-installers
test-installers: ## Run installer helper tests.
	./scripts/latest-release-tag_test.sh
	./scripts/install-k3s_test.sh

##@ Dependencies

LOCALBIN ?= $(CURDIR)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

KUSTOMIZE_VERSION ?= v5.7.1
CONTROLLER_TOOLS_VERSION ?= v0.20.0
GOLANGCI_LINT_VERSION ?= v2.12.2

ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
	[ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
	printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
	[ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
	printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download envtest binaries for the configured Kubernetes version.
	@echo "Setting up envtest binaries for Kubernetes $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path >/dev/null

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# Install a versioned tool into LOCALBIN and update the stable symlink.
# $1: target path, $2: Go package, $3: package version.
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3); \
echo "Downloading $${package}"; \
rm -f "$(1)"; \
GOBIN="$(LOCALBIN)" go install $${package}; \
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)"; \
}; \
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
