---
title: Self-host on k3s
agentPrompt: >-
  Read https://gratefulagents.dev/docs/getting-started/self-hosting-k3s/ and install gratefulagents on my server with k3s. Confirm the prerequisites, run the install, and finish by handing me the URL and sign-in credentials.
---

# Self-host on k3s

Use this guide to install the latest published GratefulAgents release on a fresh, single-node Debian or Ubuntu server. The supported installer resolves the latest GitHub release tag, pulls the matching public controller, worker, and injector images from `ghcr.io/gratefulagents`, installs agent-sandbox, and upgrades the chart with those versioned image references.

It also installs or configures Git, k3s, kubectl, Helm, PostgreSQL with pgvector, MinIO, and Jaeger. It does not install Docker or a node-local image registry.

:::warning Before exposing the dashboard
The default `LoadBalancer` service makes the dashboard available over plain HTTP on port `8090`. Restrict that port to trusted addresses during setup. For Internet-facing use, put TLS and authentication at an HTTPS ingress or reverse proxy on port `443`, then remove public access to `8090`. Do not expose the Kubernetes API (`6443`) or the GitHub webhook listener (`8091`) to the public Internet.
:::

## Server requirements

Use a dedicated server or VM with:

- Debian 12+ or Ubuntu 22.04+;
- `x86_64` or `arm64`;
- systemd and root or sudo access;
- 4 or more CPUs, 8 GiB or more RAM, and 50 GiB or more free root disk; and
- outbound HTTPS to GitHub, `get.k3s.io`, `get.helm.sh`, container registries, configured AI providers, repositories, and MCP services.

The default bundled PostgreSQL and MinIO use k3s's node-local `local-path` StorageClass. A single node has no node-level high availability. Plan encrypted backups before putting data on the server.

## Install from a checkout

Connect as the normal administrative user. Clone the repository, review the installer, and run it without a `sudo` prefix:

```bash
git clone https://github.com/gratefulagents/gratefulagents.git
cd gratefulagents
./scripts/install-k3s.sh
```

The script requests sudo only for host-level changes. A root login also works. From a checkout, `make k3s-install` and `make k3s-upgrade` run the same installer; both resolve and apply the latest published release:

```bash
make k3s-upgrade
```

The installer preserves an existing k3s installation, selects the latest release tag for all three GHCR images, fetches the Helm chart and supporting manifests from that same tag, reapplies agent-sandbox, and runs `helm upgrade --install --atomic`. It does not deploy the chart from whichever branch happens to be checked out. It disables active swap and saves the original `/etc/fstab` as `/etc/fstab.gratefulagents-backup` before its first swap change. It creates the login user's kubeconfig at `~/.kube/config`.

Older versions of this installer created a registry in the `gratefulagents-registry` namespace and configured `127.0.0.1:5000` in k3s. The current installer does not use or remove those older resources automatically.

:::warning Do not install the chart with bare defaults
The chart's default bundled PostgreSQL and MinIO credentials are sample values, and its application images use `latest`. A production installation must provide persistent non-sample values and explicit image references. The k3s installer supplies generated persistent credentials and versioned GHCR references. Do not replace it with a bare `helm install` command.
:::

## Persistent installer state and upgrades

By default, the installer creates this directory for the login user:

```text
~/.config/gratefulagents/
├── install.lock
└── gratefulagents-gratefulagents-system-values.yaml
```

The values file has mode `0600` and contains the generated PostgreSQL and MinIO credentials. It is the upgrade contract:

1. Back it up in encrypted storage.
2. Make persistent chart changes in this file.
3. Rerun `./scripts/install-k3s.sh` or `make k3s-upgrade`.
4. Restore the file before upgrading an existing release if it is lost.

The installer deliberately refuses an upgrade when the release exists but this values file is missing. Generating new credentials against existing PostgreSQL or MinIO volumes can prevent those services from starting or make stored data inaccessible. Do not use one-off `helm --set` commands for settings that must survive a later installer run.

### Configure Google sign-in persistently

Add Google settings to the same private values file, then rerun the installer. For the default state location, edit `~/.config/gratefulagents/gratefulagents-gratefulagents-system-values.yaml` and add:

```yaml
auth:
  google:
    clientID: "your-client-id.apps.googleusercontent.com"
    adminEmails: "you@example.com"
    ssoDefaultReadonly: true
```

`clientID` enables Google sign-in. `adminEmails` is a comma-separated list of Google accounts granted the admin role. With `ssoDefaultReadonly: true`, other Google users get the viewer role. Configure the dashboard's final origin in Google Cloud before enabling sign-in. Keep these values in the persistent file; do not enable Google sign-in only through an ad-hoc Helm command.

## Installer overrides

Set overrides before running the installer.

| Variable | Default | Purpose |
| --- | --- | --- |
| `AGENT_SANDBOX_VERSION` | `v0.3.10` | agent-sandbox release. |
| `CHART_DIR` | `<matching-release-source>/dist/chart` | Explicit local chart override. |
| `CLOUDFLARE_TUNNEL_TOKEN` | empty | Remotely managed tunnel token; required on first connector deployment. |
| `DASHBOARD_SERVICE_TYPE` | `LoadBalancer` | `LoadBalancer`, `ClusterIP`, or `NodePort`. |
| `GRATEFULAGENTS_REF` | selected `IMAGE_TAG` | Explicit source branch or tag override. |
| `GRATEFULAGENTS_REPOSITORY` | `gratefulagents/gratefulagents` | GitHub repository queried for the latest release. |
| `GRATEFULAGENTS_REPOSITORY_URL` | project GitHub URL | Repository cloned when no source checkout is available. |
| `GITHUB_TOKEN` | empty | Optional GitHub token used when the unauthenticated API rate limit is insufficient. |
| `HELM_VERSION` | `v3.18.6` | Helm version installed only when Helm is absent. |
| `IMAGE_REGISTRY` | `ghcr.io/gratefulagents` | Registry and namespace for controller, worker, and injector. |
| `IMAGE_TAG` | latest published release tag | Tag for all three published application images; set it to pin a release. |
| `INSTALL_CLOUDFLARE_WARP` | `0` | Set to `1` to deploy the Cloudflare Tunnel/WARP connector. |
| `K3S_CHANNEL` | `stable` | k3s channel when `K3S_VERSION` is empty. |
| `K3S_VERSION` | empty | Exact k3s release; takes precedence over the channel. |
| `NAMESPACE` | `gratefulagents-system` | Release namespace. |
| `RELEASE_NAME` | `gratefulagents` | Helm release name. |
| `SKIP_RESOURCE_CHECK` | `0` | Set to `1` to suppress minimum-resource warnings. |
| `SOURCE_DIR` | matching release checkout | Explicit local source override used for the chart and optional connector configuration. |
| `SERVER_IP` | node internal IP | Address printed in the dashboard URL for `LoadBalancer` or `NodePort`. |
| `STATE_DIR` | `<login-home>/.config/gratefulagents` | Private installer state directory. |
| `TIMEOUT` | `15m` | Kubernetes and Helm readiness timeout. |
| `VALUES_FILE` | `<state-dir>/<release>-<namespace>-values.yaml` | Private persistent values file. |

To reproduce a particular application release, set `IMAGE_TAG` to its version tag; the installer automatically fetches the chart at that tag. Set `GRATEFULAGENTS_REF`, `SOURCE_DIR`, or `CHART_DIR` only when you intentionally need a different or local chart revision. Pin `K3S_VERSION` only after validating that Kubernetes version with the selected release.

## Cloudflare and network exposure

The optional connector deploys `cloudflared` in the `cloudflare-warp` namespace. It can connect a remotely managed Cloudflare Tunnel or advertise private-network routes to enrolled WARP clients. It does **not** create a Cloudflare DNS record, public hostname, Access policy, ingress, or private-network route for you.

For a first connector deployment:

```bash
INSTALL_CLOUDFLARE_WARP=1 \
CLOUDFLARE_TUNNEL_TOKEN='replace-with-tunnel-token' \
./scripts/install-k3s.sh
```

The token is stored in the `cloudflare-tunnel-token` Secret. Later runs reuse that Secret when the token is omitted. Treat both the input token and Secret as sensitive. Configure routing and access policy in Cloudflare after the connector is healthy. For an end-to-end public hostname, Access policy, and installed-app service token, follow [Publish securely with Cloudflare](./cloudflare-access.md).

For private access through a local port-forward, select `ClusterIP`:

```bash
DASHBOARD_SERVICE_TYPE=ClusterIP ./scripts/install-k3s.sh
kubectl -n gratefulagents-system port-forward \
  service/gratefulagents-controller-manager-dashboard 8090:8090
```

Then open `http://localhost:8090` on the administrative machine.

The installer does not change host or provider firewalls. Allow inbound traffic only for the exposure mode you select:

| Port | Inbound source | Purpose |
| --- | --- | --- |
| SSH port, commonly `22/TCP` | Administrative CIDRs | Server administration. |
| `8090/TCP` | Trusted user CIDRs only | Temporary quick dashboard access with `LoadBalancer`. Remove after HTTPS ingress works. |
| `80/TCP`, `443/TCP` | Dashboard users | Optional redirect and recommended TLS/authenticated ingress. |
| `6443/TCP` | Trusted admins/nodes only | Remote Kubernetes administration or additional nodes; never public. |
| `8091/TCP` | Do not expose directly | GitHub webhook service. Route only the required webhook path through public HTTPS and validate GitHub signatures with the webhook secret. Do not put an interactive login gate in front of GitHub's callback. |
| `8472/UDP`, `10250/TCP` | Trusted cluster nodes only | Multi-node k3s traffic; not required for one node. |

PostgreSQL (`5432`), MinIO (`9000`), and Jaeger (`4317`/`16686`) must not be publicly exposed. If using a provider firewall and a host firewall, configure both. Cloudflare Tunnel does not remove the need to restrict direct origin access.

## Status, credentials, and logs

The supported status target shows nodes, namespaces, and deployments:

```bash
make k3s-status
```

For more detail:

```bash
kubectl -n gratefulagents-system get pods,pvc,svc
helm -n gratefulagents-system status gratefulagents
kubectl -n gratefulagents-system logs \
  -l control-plane=controller-manager -c manager --tail=200
```

Retrieve the generated local admin password only on a trusted terminal:

```bash
kubectl -n gratefulagents-system get secret gratefulagents-admin-credentials \
  -o jsonpath='{.data.password}' | base64 -d; echo
```

## Removal and retained data

This release does not provide a supported automated k3s uninstall command. Removing k3s, Helm resources, namespaces, persistent volumes, or the server requires an operator-specific plan and can permanently delete data. Back up PostgreSQL, MinIO, and the private values file before removal.

Helm keeps chart CRDs by default (`crd.keep: true`). Uninstalling a release therefore does not remove those CRDs or their custom resources. Do not delete CRDs until you have inventoried and exported any `AgentRun`, trigger, project, and configuration resources you need. Removing a CRD also deletes all custom resources of that type from the cluster.

For production planning, consider external PostgreSQL and object storage, persistent tracing, pinned dependency images, backups, monitoring, TLS, and a Kubernetes design appropriate to your availability requirements.
