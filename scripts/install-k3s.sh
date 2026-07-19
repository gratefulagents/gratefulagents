#!/usr/bin/env bash
# Install gratefulagents and all bundled dependencies on a fresh Debian/Ubuntu
# server using a single-node k3s cluster.
set -Eeuo pipefail

AGENT_SANDBOX_VERSION="${AGENT_SANDBOX_VERSION:-v0.3.10}"
CLOUDFLARE_TUNNEL_TOKEN="${CLOUDFLARE_TUNNEL_TOKEN:-}"
DASHBOARD_SERVICE_TYPE="${DASHBOARD_SERVICE_TYPE:-LoadBalancer}"
GRATEFULAGENTS_REF="${GRATEFULAGENTS_REF:-main}"
GRATEFULAGENTS_REPOSITORY_URL="${GRATEFULAGENTS_REPOSITORY_URL:-https://github.com/gratefulagents/gratefulagents.git}"
HELM_VERSION="${HELM_VERSION:-v3.18.6}"
IMAGE_TAG="${IMAGE_TAG:-}"
INSTALL_CLOUDFLARE_WARP="${INSTALL_CLOUDFLARE_WARP:-0}"
K3S_CHANNEL="${K3S_CHANNEL:-stable}"
K3S_VERSION="${K3S_VERSION:-}"
NAMESPACE="${NAMESPACE:-gratefulagents-system}"
RELEASE_NAME="${RELEASE_NAME:-gratefulagents}"
SOURCE_DIR="${SOURCE_DIR:-}"
TIMEOUT="${TIMEOUT:-15m}"
CHART_DIR="${CHART_DIR:-}"
SKIP_RESOURCE_CHECK="${SKIP_RESOURCE_CHECK:-0}"
LOCAL_REGISTRY="127.0.0.1:5000"

usage() {
  cat <<'EOF'
Install gratefulagents on a fresh Debian/Ubuntu server with k3s.

Usage:
  ./scripts/install-k3s.sh

Run this as the login user; the script asks for sudo when needed. It is safe to
run again: k3s and the local image registry are kept, all application images are
rebuilt, PostgreSQL/MinIO credentials are reused, and Helm upgrades the release.

Environment overrides:
  AGENT_SANDBOX_VERSION   agent-sandbox release (default: v0.3.10)
  CHART_DIR               local chart directory (auto-detected in a checkout)
  CLOUDFLARE_TUNNEL_TOKEN remotely-managed tunnel token; required the first time
                          INSTALL_CLOUDFLARE_WARP=1 is used
  DASHBOARD_SERVICE_TYPE  LoadBalancer, ClusterIP, or NodePort
                          (default: LoadBalancer)
  GRATEFULAGENTS_REF      branch/tag cloned if no local chart exists (default: main)
  HELM_VERSION            Helm version (default: v3.18.6)
  IMAGE_TAG               local registry image tag (default: source Git commit)
  INSTALL_CLOUDFLARE_WARP deploy a cloudflared Tunnel/WARP connector when set to 1
  K3S_CHANNEL             k3s channel when K3S_VERSION is empty (default: stable)
  K3S_VERSION             exact k3s release, for example v1.33.5+k3s1
  NAMESPACE               release namespace (default: gratefulagents-system)
  RELEASE_NAME            Helm release name (default: gratefulagents)
  SOURCE_DIR              source checkout used to build images (auto-detected)
  TIMEOUT                 Kubernetes/Helm timeout (default: 15m)
  SKIP_RESOURCE_CHECK=1   suppress minimum-resource warnings

The installer does not edit cloud/provider firewalls. Open TCP 8090 for the
quick HTTP setup, or keep it private and put an HTTPS ingress on TCP 443.
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi
if [[ $# -ne 0 ]]; then
  echo "error: unknown argument '$1' (try --help)" >&2
  exit 2
fi

log() { printf '\n==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

if [[ $EUID -eq 0 ]]; then
  ROOT=()
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    INSTALL_USER="$SUDO_USER"
  else
    INSTALL_USER="root"
  fi
else
  command -v sudo >/dev/null 2>&1 || die "sudo is required when not running as root"
  ROOT=(sudo)
  INSTALL_USER="$(id -un)"
fi

INSTALL_HOME="$(getent passwd "$INSTALL_USER" | cut -d: -f6)"
INSTALL_GROUP="$(id -gn "$INSTALL_USER")"
[[ -n "$INSTALL_HOME" ]] || die "could not determine home directory for $INSTALL_USER"

[[ "$NAMESPACE" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "NAMESPACE must be a DNS label"
[[ "$RELEASE_NAME" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "RELEASE_NAME must be a DNS label"
case "$INSTALL_CLOUDFLARE_WARP" in
  0|1) ;;
  *) die "INSTALL_CLOUDFLARE_WARP must be 0 or 1" ;;
esac
case "$DASHBOARD_SERVICE_TYPE" in
  LoadBalancer|ClusterIP|NodePort) ;;
  *) die "DASHBOARD_SERVICE_TYPE must be LoadBalancer, ClusterIP, or NodePort" ;;
esac

[[ -r /etc/os-release ]] || die "/etc/os-release is missing"
# shellcheck disable=SC1091
source /etc/os-release
case "${ID:-}" in
  debian|ubuntu) ;;
  *) die "supported operating systems are Debian and Ubuntu (found: ${ID:-unknown})" ;;
esac
command -v systemctl >/dev/null 2>&1 || die "systemd is required"

case "$(uname -m)" in
  x86_64) HELM_ARCH="amd64" ;;
  aarch64|arm64) HELM_ARCH="arm64" ;;
  *) die "supported architectures are x86_64 and arm64" ;;
esac

STATE_DIR="${STATE_DIR:-$INSTALL_HOME/.config/gratefulagents}"
VALUES_FILE="${VALUES_FILE:-$STATE_DIR/${RELEASE_NAME}-${NAMESPACE}-values.yaml}"
mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"
if [[ $EUID -eq 0 && "$INSTALL_USER" != "root" ]]; then
  chown "$INSTALL_USER:$INSTALL_GROUP" "$STATE_DIR"
fi
exec 9>"$STATE_DIR/install.lock"
flock -n 9 || die "another gratefulagents installation is running"

TMP_DIR="$(mktemp -d)"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

if [[ "$SKIP_RESOURCE_CHECK" != "1" ]]; then
  cpu_count="$(nproc)"
  memory_kib="$(awk '/MemTotal:/ {print $2}' /proc/meminfo)"
  disk_kib="$(df -Pk / | awk 'NR==2 {print $4}')"
  (( cpu_count >= 4 )) || warn "4+ CPUs recommended; found $cpu_count"
  (( memory_kib >= 8 * 1024 * 1024 )) || warn "8+ GiB RAM recommended"
  (( disk_kib >= 50 * 1024 * 1024 )) || warn "50+ GiB free disk recommended on the root filesystem"
fi

"$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/install-k3s-dependencies.sh"

command -v git >/dev/null 2>&1 || die "git installation failed"
if docker info >/dev/null 2>&1; then
  CONTAINER_TOOL="env DOCKER_BUILDKIT=1 docker"
elif "${ROOT[@]}" docker info >/dev/null 2>&1; then
  CONTAINER_TOOL="${ROOT[*]} env DOCKER_BUILDKIT=1 docker"
else
  die "Docker is required to build the controller, worker, and injector images"
fi

# Kubernetes rejects active swap with its default kubelet configuration. Keep a
# one-time backup before making the standard persistent change.
if [[ -n "$(swapon --show --noheadings 2>/dev/null)" ]]; then
  log "Disabling swap for Kubernetes"
  "${ROOT[@]}" swapoff -a
fi
if grep -Eq '^[[:space:]]*[^#].*[[:space:]]swap[[:space:]]' /etc/fstab; then
  [[ -e /etc/fstab.gratefulagents-backup ]] || "${ROOT[@]}" cp /etc/fstab /etc/fstab.gratefulagents-backup
  "${ROOT[@]}" sed -ri '/^[[:space:]]*#/! { /[[:space:]]swap[[:space:]]/ s|^|# disabled by gratefulagents installer: |; }' /etc/fstab
fi

if ! command -v k3s >/dev/null 2>&1; then
  log "Installing k3s (${K3S_VERSION:-channel: $K3S_CHANNEL})"
  curl --fail --silent --show-error --location https://get.k3s.io -o "$TMP_DIR/install-k3s.sh"
  chmod 700 "$TMP_DIR/install-k3s.sh"
  if [[ -n "$K3S_VERSION" ]]; then
    "${ROOT[@]}" env INSTALL_K3S_VERSION="$K3S_VERSION" sh "$TMP_DIR/install-k3s.sh"
  else
    "${ROOT[@]}" env INSTALL_K3S_CHANNEL="$K3S_CHANNEL" sh "$TMP_DIR/install-k3s.sh"
  fi
else
  log "Keeping existing k3s installation: $(k3s --version | head -n1)"
  "${ROOT[@]}" systemctl enable --now k3s
fi

if ! command -v kubectl >/dev/null 2>&1; then
  log "Installing the kubectl command provided by k3s"
  "${ROOT[@]}" ln -sf /usr/local/bin/k3s /usr/local/bin/kubectl
  hash -r
fi
command -v kubectl >/dev/null 2>&1 || die "kubectl installation failed"

# k3s/containerd defaults to HTTPS for explicit registries. Add the local
# loopback registry as an HTTP mirror while preserving any existing mirrors or
# authentication configuration in registries.yaml.
registry_config=/etc/rancher/k3s/registries.yaml
registry_config_copy="$TMP_DIR/registries.yaml.current"
registry_config_next="$TMP_DIR/registries.yaml.next"
registry_config_changed=0
if "${ROOT[@]}" test -f "$registry_config"; then
  "${ROOT[@]}" cat "$registry_config" >"$registry_config_copy"
else
  : >"$registry_config_copy"
fi

if grep -Fq "$LOCAL_REGISTRY" "$registry_config_copy"; then
  grep -Fq "http://$LOCAL_REGISTRY" "$registry_config_copy" || \
    die "$registry_config already configures $LOCAL_REGISTRY without its required HTTP endpoint"
elif [[ -s "$registry_config_copy" ]]; then
  if grep -Eq '^mirrors:[[:space:]]*$' "$registry_config_copy"; then
    awk -v registry="$LOCAL_REGISTRY" '
      { print }
      !added && /^mirrors:[[:space:]]*$/ {
        printf "  \"%s\":\n    endpoint:\n      - \"http://%s\"\n", registry, registry
        added = 1
      }
    ' "$registry_config_copy" >"$registry_config_next"
  elif grep -Eq '^mirrors:' "$registry_config_copy"; then
    die "$registry_config uses an inline mirrors value; add an HTTP endpoint for $LOCAL_REGISTRY manually"
  else
    cp "$registry_config_copy" "$registry_config_next"
    cat >>"$registry_config_next" <<EOF

mirrors:
  "$LOCAL_REGISTRY":
    endpoint:
      - "http://$LOCAL_REGISTRY"
EOF
  fi
  registry_config_changed=1
else
  cat >"$registry_config_next" <<EOF
# Managed by the gratefulagents k3s installer.
mirrors:
  "$LOCAL_REGISTRY":
    endpoint:
      - "http://$LOCAL_REGISTRY"
EOF
  registry_config_changed=1
fi

if [[ "$registry_config_changed" == "1" ]]; then
  log "Configuring k3s to pull from the local registry"
  "${ROOT[@]}" install -D -m 0600 "$registry_config_next" "$registry_config"
  "${ROOT[@]}" systemctl restart k3s
fi

log "Configuring kubectl for $INSTALL_USER"
"${ROOT[@]}" mkdir -p "$INSTALL_HOME/.kube"
"${ROOT[@]}" cp /etc/rancher/k3s/k3s.yaml "$INSTALL_HOME/.kube/config"
"${ROOT[@]}" chown -R "$INSTALL_USER:$INSTALL_GROUP" "$INSTALL_HOME/.kube"
chmod 600 "$INSTALL_HOME/.kube/config"
export KUBECONFIG="$INSTALL_HOME/.kube/config"

kubectl version --client >/dev/null
kubectl wait --for=condition=Ready nodes --all --timeout="$TIMEOUT"
kubectl get storageclass local-path >/dev/null 2>&1 || die "k3s local-path StorageClass was not created"

if ! command -v helm >/dev/null 2>&1; then
  log "Installing Helm $HELM_VERSION"
  helm_archive="helm-${HELM_VERSION}-linux-${HELM_ARCH}.tar.gz"
  curl --fail --silent --show-error --location "https://get.helm.sh/${helm_archive}" -o "$TMP_DIR/$helm_archive"
  curl --fail --silent --show-error --location "https://get.helm.sh/${helm_archive}.sha256sum" -o "$TMP_DIR/$helm_archive.sha256sum"
  (
    cd "$TMP_DIR"
    sha256sum --check "$helm_archive.sha256sum"
  )
  tar -xzf "$TMP_DIR/$helm_archive" -C "$TMP_DIR"
  "${ROOT[@]}" install -m 0755 "$TMP_DIR/linux-${HELM_ARCH}/helm" /usr/local/bin/helm
else
  log "Keeping existing Helm installation: $(helm version --short)"
fi
helm_version="$(helm version --template '{{.Version}}')"
[[ "$helm_version" == v3.* ]] || die "Helm 3 is required (found $helm_version)"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [[ -z "$SOURCE_DIR" && -f "$script_dir/../Makefile" && -f "$script_dir/../Dockerfile.worker" ]]; then
  SOURCE_DIR="$(cd "$script_dir/.." && pwd)"
fi
if [[ -z "$SOURCE_DIR" && -n "$CHART_DIR" ]]; then
  source_candidate="$(cd "$CHART_DIR/../.." 2>/dev/null && pwd || true)"
  if [[ -f "$source_candidate/Makefile" && -f "$source_candidate/Dockerfile.worker" ]]; then
    SOURCE_DIR="$source_candidate"
  fi
fi
if [[ -z "$SOURCE_DIR" ]]; then
  log "Fetching gratefulagents $GRATEFULAGENTS_REF"
  git clone --depth 1 --branch "$GRATEFULAGENTS_REF" "$GRATEFULAGENTS_REPOSITORY_URL" "$TMP_DIR/gratefulagents"
  SOURCE_DIR="$TMP_DIR/gratefulagents"
fi
SOURCE_DIR="$(cd "$SOURCE_DIR" && pwd)"
if [[ -z "$CHART_DIR" ]]; then
  CHART_DIR="$SOURCE_DIR/dist/chart"
else
  CHART_DIR="$(cd "$CHART_DIR" && pwd)"
fi
[[ -f "$SOURCE_DIR/Makefile" ]] || die "Makefile not found in source directory $SOURCE_DIR"
[[ -f "$SOURCE_DIR/Dockerfile" ]] || die "manager Dockerfile not found in $SOURCE_DIR"
[[ -f "$SOURCE_DIR/Dockerfile.worker" ]] || die "worker Dockerfile not found in $SOURCE_DIR"
[[ -f "$SOURCE_DIR/Dockerfile.injector" ]] || die "injector Dockerfile not found in $SOURCE_DIR"
[[ -f "$CHART_DIR/Chart.yaml" ]] || die "Helm chart not found at $CHART_DIR"

if [[ -z "$IMAGE_TAG" ]]; then
  IMAGE_TAG="$(git -C "$SOURCE_DIR" rev-parse HEAD)"
fi
[[ "$IMAGE_TAG" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || die "IMAGE_TAG is not a valid container image tag"
manager_image_repository="$LOCAL_REGISTRY/gratefulagents/controller"
manager_image="$manager_image_repository:$IMAGE_TAG"
worker_image="$LOCAL_REGISTRY/gratefulagents/worker:$IMAGE_TAG"
injector_image="$LOCAL_REGISTRY/gratefulagents/injector:$IMAGE_TAG"

log "Deploying the in-cluster registry and building all images"
make -C "$SOURCE_DIR" local-build-push \
  CONTAINER_TOOL="$CONTAINER_TOOL" \
  IMAGE_TAG="$IMAGE_TAG" \
  LOCAL_REGISTRY="$LOCAL_REGISTRY"
curl --fail --silent --show-error "http://$LOCAL_REGISTRY/v2/" >/dev/null || \
  die "the local image registry is not reachable at http://$LOCAL_REGISTRY"

log "Installing agent-sandbox $AGENT_SANDBOX_VERSION"
manifest_base="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}"
curl --fail --silent --show-error --location "$manifest_base/manifest.yaml" -o "$TMP_DIR/agent-sandbox-manifest.yaml"
curl --fail --silent --show-error --location "$manifest_base/extensions.yaml" -o "$TMP_DIR/agent-sandbox-extensions.yaml"
kubectl apply -f "$TMP_DIR/agent-sandbox-manifest.yaml"
kubectl apply -f "$TMP_DIR/agent-sandbox-extensions.yaml"
kubectl -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout="$TIMEOUT"

if [[ ! -f "$VALUES_FILE" ]] && helm status "$RELEASE_NAME" --namespace "$NAMESPACE" >/dev/null 2>&1; then
  die "release $NAMESPACE/$RELEASE_NAME exists but $VALUES_FILE is missing; restore the file before upgrading so persistent-service credentials are not rotated"
fi
if [[ ! -f "$VALUES_FILE" ]]; then
  log "Generating persistent bundled-service credentials"
  postgres_password="$(openssl rand -hex 24)"
  minio_password="$(openssl rand -hex 24)"
  umask 077
  cat >"$VALUES_FILE" <<EOF
# Generated by scripts/install-k3s.sh. Keep this file private and backed up.
# Edit values here, then rerun the installer to apply an upgrade.
postgres:
  auth:
    username: gratefulagents
    password: "$postgres_password"
    database: gratefulagents
minio:
  rootUser: gratefulagents
  rootPassword: "$minio_password"
EOF
  chmod 600 "$VALUES_FILE"
  if [[ $EUID -eq 0 && "$INSTALL_USER" != "root" ]]; then
    chown "$INSTALL_USER:$INSTALL_GROUP" "$VALUES_FILE"
  fi
else
  log "Reusing installer values from $VALUES_FILE"
fi

log "Installing gratefulagents"
helm lint "$CHART_DIR" --values "$VALUES_FILE" \
  --set "dashboard.service.type=$DASHBOARD_SERVICE_TYPE" \
  --set-string "manager.image.repository=$manager_image_repository" \
  --set-string "manager.image.tag=$IMAGE_TAG" \
  --set-string "agentImages.worker=$worker_image" \
  --set-string "agentImages.injector=$injector_image"
helm upgrade --install "$RELEASE_NAME" "$CHART_DIR" \
  --namespace "$NAMESPACE" --create-namespace \
  --values "$VALUES_FILE" \
  --set "dashboard.service.type=$DASHBOARD_SERVICE_TYPE" \
  --set-string "manager.image.repository=$manager_image_repository" \
  --set-string "manager.image.tag=$IMAGE_TAG" \
  --set-string "agentImages.worker=$worker_image" \
  --set-string "agentImages.injector=$injector_image" \
  --atomic --wait --wait-for-jobs --timeout "$TIMEOUT" --history-max 10

manager_deployment="$(kubectl -n "$NAMESPACE" get deployment \
  -l "app.kubernetes.io/instance=$RELEASE_NAME,control-plane=controller-manager" \
  -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$manager_deployment" ]] || die "controller-manager Deployment was not found"
kubectl -n "$NAMESPACE" rollout status "deployment/$manager_deployment" --timeout="$TIMEOUT"
kubectl -n "$NAMESPACE" get secret gratefulagents-admin-credentials >/dev/null

cloudflare_status="disabled"
if [[ "$INSTALL_CLOUDFLARE_WARP" == "1" ]]; then
  log "Installing the optional Cloudflare Tunnel/WARP connector"
  if [[ -n "$CLOUDFLARE_TUNNEL_TOKEN" ]]; then
    token_file="$TMP_DIR/cloudflare-tunnel-token"
    umask 077
    printf '%s' "$CLOUDFLARE_TUNNEL_TOKEN" >"$token_file"
    kubectl create namespace cloudflare-warp --dry-run=client -o yaml | kubectl apply -f -
    kubectl -n cloudflare-warp create secret generic cloudflare-tunnel-token \
      --from-file=token="$token_file" --dry-run=client -o yaml | kubectl apply -f -
  elif ! kubectl -n cloudflare-warp get secret cloudflare-tunnel-token >/dev/null 2>&1; then
    die "CLOUDFLARE_TUNNEL_TOKEN is required the first time INSTALL_CLOUDFLARE_WARP=1 is used"
  fi
  kubectl apply -f "$SOURCE_DIR/config/cloudflare/cloudflared.yaml"
  kubectl -n cloudflare-warp rollout status deployment/cloudflared --timeout="$TIMEOUT"
  cloudflare_status="enabled (namespace cloudflare-warp)"
fi

dashboard_service="$(kubectl -n "$NAMESPACE" get service \
  -l "app.kubernetes.io/instance=$RELEASE_NAME" \
  -o jsonpath='{.items[?(@.spec.ports[0].name=="dashboard")].metadata.name}')"
[[ -n "$dashboard_service" ]] || die "dashboard Service was not found"
node_ip="${SERVER_IP:-$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')}"
case "$DASHBOARD_SERVICE_TYPE" in
  LoadBalancer)
    dashboard_access="http://${node_ip}:8090"
    firewall_hint="Allow inbound TCP 8090 from trusted user addresses."
    ;;
  NodePort)
    node_port="$(kubectl -n "$NAMESPACE" get service "$dashboard_service" -o jsonpath='{.spec.ports[0].nodePort}')"
    dashboard_access="http://${node_ip}:${node_port}"
    firewall_hint="Allow inbound TCP $node_port from trusted user addresses."
    ;;
  ClusterIP)
    dashboard_access="Run: kubectl -n $NAMESPACE port-forward service/$dashboard_service 8090:8090; then open http://localhost:8090"
    firewall_hint="No public dashboard port is required for local port-forwarding."
    ;;
esac
cat <<EOF

Installation complete.

Dashboard: $dashboard_access
Username:  admin
Password:  kubectl -n $NAMESPACE get secret gratefulagents-admin-credentials \\
             -o jsonpath='{.data.password}' | base64 -d; echo

Installer values (contains database/storage credentials):
  $VALUES_FILE

Images:    $LOCAL_REGISTRY/gratefulagents/*:$IMAGE_TAG
Registry:  http://$LOCAL_REGISTRY (node loopback only)
Cloudflare Tunnel/WARP connector: $cloudflare_status

Next:
  1. $firewall_hint
  2. Sign in and add AI-provider and GitHub credentials under Settings.
  3. For an Internet-facing production install, put TLS/authenticated ingress on
     TCP 443 and remove public access to 8090. Do not expose 6443 or 8091.
EOF
