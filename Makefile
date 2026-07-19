# Local Kind and self-hosted k3s workflows.
#
# Use `make k3s-install` for first-time setup and `make k3s-upgrade` to rebuild
# the local images and apply the current checkout. The installer supplies the
# registry address to `local-build-push`; this Makefile intentionally contains
# no host address, remote-server, cloud-registry, or credential configuration.

CONTAINER_TOOL ?= docker
KUBECTL ?= kubectl
REGISTRY_NAMESPACE ?= gratefulagents-registry
LOCAL_REGISTRY ?=
IMAGE_TAG ?= $(shell git rev-parse HEAD)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show the supported self-hosting commands.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: k3s-prereqs
k3s-prereqs: ## Install Debian/Ubuntu prerequisites for k3s management.
	./scripts/install-k3s-dependencies.sh

.PHONY: kind-install
kind-install: ## Install or update the v0.1.0 release in a local Kind cluster.
	./scripts/install-kind.sh

.PHONY: k3s-install
k3s-install: ## Install or update this checkout on a self-hosted k3s server.
	./scripts/install-k3s.sh

.PHONY: k3s-upgrade
k3s-upgrade: ## Rebuild images and apply the current checkout to k3s.
	./scripts/install-k3s.sh

.PHONY: k3s-status
k3s-status: ## Show Kubernetes nodes, the application namespace, and workloads.
	$(KUBECTL) get nodes
	$(KUBECTL) get namespaces
	$(KUBECTL) get deployments --all-namespaces

.PHONY: local-build-push
local-build-push: ## Build and push images to the installer-provided local registry.
	@test -n "$(LOCAL_REGISTRY)" || { echo "LOCAL_REGISTRY is required" >&2; exit 2; }
	$(KUBECTL) apply -f config/registry/registry.yaml
	$(KUBECTL) -n $(REGISTRY_NAMESPACE) rollout status deployment/registry --timeout=180s
	$(MAKE) docker-build-all \
		IMG="$(LOCAL_REGISTRY)/gratefulagents/controller:$(IMAGE_TAG)" \
		WORKER_IMG="$(LOCAL_REGISTRY)/gratefulagents/worker:$(IMAGE_TAG)" \
		INJECTOR_IMG="$(LOCAL_REGISTRY)/gratefulagents/injector:$(IMAGE_TAG)"
	$(MAKE) docker-push-all \
		IMG="$(LOCAL_REGISTRY)/gratefulagents/controller:$(IMAGE_TAG)" \
		WORKER_IMG="$(LOCAL_REGISTRY)/gratefulagents/worker:$(IMAGE_TAG)" \
		INJECTOR_IMG="$(LOCAL_REGISTRY)/gratefulagents/injector:$(IMAGE_TAG)"

.PHONY: docker-build-all docker-build docker-build-worker docker-build-injector
docker-build-all: docker-build docker-build-worker docker-build-injector

docker-build:
	$(CONTAINER_TOOL) build -t $(IMG) .

docker-build-worker:
	$(CONTAINER_TOOL) build -t $(WORKER_IMG) -f Dockerfile.worker .

docker-build-injector:
	$(CONTAINER_TOOL) build -t $(INJECTOR_IMG) -f Dockerfile.injector .

.PHONY: docker-push-all docker-push docker-push-worker docker-push-injector
docker-push-all: docker-push docker-push-worker docker-push-injector

docker-push:
	$(CONTAINER_TOOL) push $(IMG)

docker-push-worker:
	$(CONTAINER_TOOL) push $(WORKER_IMG)

docker-push-injector:
	$(CONTAINER_TOOL) push $(INJECTOR_IMG)
