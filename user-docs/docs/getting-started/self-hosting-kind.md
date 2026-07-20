---
title: Run locally with Kind
agentPrompt: >-
  Read https://gratefulagents.dev/docs/getting-started/self-hosting-kind/ and install gratefulagents on my laptop with Kind. Verify Docker and the other prerequisites first, run the installer, then give me the URL and the credentials I need to sign in.
---

# Run locally with Kind

Use the Kind installer to evaluate the latest published GratefulAgents release on a macOS or Linux laptop. It creates a single-node Kubernetes cluster in Docker, installs agent-sandbox and the bundled PostgreSQL, MinIO, and Jaeger services, resolves the latest GitHub release tag, and deploys the matching public images without a local image build or registry:

```text
ghcr.io/gratefulagents/controller:<release-tag>
ghcr.io/gratefulagents/worker:<release-tag>
ghcr.io/gratefulagents/injector:<release-tag>
```

This is a local evaluation path, not a production hosting guide.

## Requirements

Before installing, provide a running Docker-compatible runtime:

- Docker Desktop, OrbStack, or Colima on macOS; or
- Docker Engine or Docker Desktop on Linux.

Use an `x86_64`/AMD64 or `arm64`/AArch64 host. The installer requires `curl`, `openssl`, and `tar`; it downloads pinned Kind, kubectl, and Helm binaries when needed. It does not use sudo or modify your default kubeconfig.

## Install the latest release

Clone the `main` branch and run the supported Make target:

```bash
git clone --branch main --depth 1 \
  https://github.com/gratefulagents/gratefulagents.git
cd gratefulagents
make kind-install
```

When installation completes, open the printed `http://127.0.0.1:8090` dashboard and retrieve the generated `admin` password using the printed command.

The installer is safe to rerun. It preserves the cluster and persistent volumes, reapplies agent-sandbox, reuses its private Helm values, and performs a Helm upgrade. It refuses to upgrade an existing release when the values file is missing, because replacement bundled-service credentials would not match existing data.

:::warning Do not use a bare chart install for this path
The chart defaults include sample PostgreSQL and MinIO credentials and `latest` image tags. The Kind installer supplies explicit versioned release image references and generates private bundled-service credentials. Do not substitute `helm install` with default values for a real deployment.
:::

## State and configuration

The installer stores sensitive state separately from your normal kubeconfig. Defaults use XDG locations when set.

| Item | Default path | Purpose |
| --- | --- | --- |
| State directory | `${XDG_CONFIG_HOME:-~/.config}/gratefulagents/kind` | Directory mode `0700`; holds the dedicated kubeconfig and values file. |
| Dedicated kubeconfig | `<state-dir>/gratefulagents-kubeconfig` | Used only by this installer and its commands. |
| Persistent values | `<state-dir>/gratefulagents-gratefulagents-system-values.yaml` | Private generated PostgreSQL and MinIO credentials; mode `0600`. Back it up only in encrypted storage. |
| Tool directory | `${XDG_CACHE_HOME:-~/.cache}/gratefulagents/bin` | Installer-managed Kind, kubectl, and Helm binaries. |

Set an override before the installer command. These are all installer configuration inputs:

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENT_SANDBOX_VERSION` | `v0.3.10` | agent-sandbox release. |
| `CHART_DIR` | auto-detected | Local Helm chart directory. |
| `CLUSTER_NAME` | `gratefulagents` | Kind cluster name. |
| `DASHBOARD_PORT` | `8090` | Localhost dashboard port. |
| `GRATEFULAGENTS_REF` | `main` | Chart ref cloned only when no local chart is found. |
| `GRATEFULAGENTS_REPOSITORY` | `gratefulagents/gratefulagents` | GitHub repository queried for the latest release. |
| `GRATEFULAGENTS_REPOSITORY_URL` | project GitHub URL | Repository used when the chart must be cloned. |
| `GITHUB_TOKEN` | empty | Optional GitHub token used when the unauthenticated API rate limit is insufficient. |
| `HELM_VERSION` | `v3.18.6` | Installer-managed Helm version. |
| `IMAGE_REGISTRY` | `ghcr.io/gratefulagents` | Registry and namespace for controller, worker, and injector. |
| `IMAGE_TAG` | latest published release tag | Tag for all three published application images; set it to pin a release. |
| `KIND_NODE_IMAGE` | pinned Kubernetes v1.33.1 Kind node image | Kind control-plane image. |
| `KIND_VERSION` | `v0.29.0` | Installer-managed Kind version. |
| `KUBECONFIG_FILE` | `<state-dir>/<cluster-name>-kubeconfig` | Dedicated kubeconfig location. |
| `KUBECTL_VERSION` | `v1.33.1` | Installer-managed kubectl version. |
| `NAMESPACE` | `gratefulagents-system` | Helm release namespace. |
| `RELEASE_NAME` | `gratefulagents` | Helm release name. |
| `STATE_DIR` | XDG config path above | State directory. |
| `TIMEOUT` | `15m` | Cluster, Helm, and workload readiness timeout. |
| `TOOL_DIR` | XDG cache path above | Installer-managed CLI directory. |
| `VALUES_FILE` | `<state-dir>/<release>-<namespace>-values.yaml` | Private persistent Helm values file. |
| `XDG_CACHE_HOME` | `~/.cache` | Base directory used when `TOOL_DIR` is unset. |
| `XDG_CONFIG_HOME` | `~/.config` | Base directory used when `STATE_DIR` is unset. |

For example, create a separate cluster and use a different local dashboard port:

```bash
CLUSTER_NAME=gratefulagents-dev \
DASHBOARD_PORT=18090 \
./scripts/install-kind.sh
```

The port mapping is created with the cluster. Delete and recreate a cluster before changing its `DASHBOARD_PORT`.

## Operate the local cluster

Use the dedicated tools and kubeconfig in a new shell:

```bash
export PATH="$HOME/.cache/gratefulagents/bin:$PATH"
export KUBECONFIG="$HOME/.config/gratefulagents/kind/gratefulagents-kubeconfig"

kubectl get nodes
kubectl -n agent-sandbox-system get pods
kubectl -n gratefulagents-system get pods,pvc,svc
helm -n gratefulagents-system status gratefulagents
```

If you changed state, tool, cluster, namespace, or release overrides, adjust these paths and names to match.

## Remove the local installation

Delete the Kind cluster to remove its containers, workloads, and persistent volumes:

```bash
kind delete cluster --name gratefulagents
```

Then remove saved installer state if you no longer need the kubeconfig or generated credentials:

```bash
rm -rf "$HOME/.config/gratefulagents/kind"
```

You may keep or separately remove `~/.cache/gratefulagents/bin`. Deleting the Kind cluster removes its local persistent volumes; export anything you need first.
