# Local Kind and self-hosted k3s workflows. Both installers deploy the latest
# published release images from GHCR unless IMAGE_TAG is explicitly set.

CONTAINER_TOOL ?= docker
KUBECTL ?= kubectl

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show the supported self-hosting commands.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

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
k3s-status: ## Show Kubernetes nodes, the application namespace, and workloads.
	$(KUBECTL) get nodes
	$(KUBECTL) get namespaces
	$(KUBECTL) get deployments --all-namespaces

.PHONY: test-installers
test-installers: ## Run installer helper tests.
	./scripts/latest-release-tag_test.sh
	./scripts/install-k3s_test.sh

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
