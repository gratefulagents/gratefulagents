#!/usr/bin/env bash
# Install gratefulagents from published images into a local Kind cluster.
set -Eeuo pipefail

AGENT_SANDBOX_VERSION="${AGENT_SANDBOX_VERSION:-v0.3.10}"
CHART_DIR="${CHART_DIR:-}"
CLUSTER_NAME="${CLUSTER_NAME:-gratefulagents}"
DASHBOARD_PORT="${DASHBOARD_PORT:-8090}"
GRATEFULAGENTS_REPOSITORY_URL="${GRATEFULAGENTS_REPOSITORY_URL:-https://github.com/gratefulagents/gratefulagents.git}"
HELM_VERSION="${HELM_VERSION:-v3.18.6}"
IMAGE_REGISTRY="${IMAGE_REGISTRY:-ghcr.io/gratefulagents}"
IMAGE_TAG="${IMAGE_TAG:-v0.1.0}"
GRATEFULAGENTS_REF="${GRATEFULAGENTS_REF:-$IMAGE_TAG}"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.33.1@sha256:050072256b9a903bd914c0b2866828150cb229cea0efe5892e2b644d5dd3b34f}"
KIND_VERSION="${KIND_VERSION:-v0.29.0}"
KUBECTL_VERSION="${KUBECTL_VERSION:-v1.33.1}"
NAMESPACE="${NAMESPACE:-gratefulagents-system}"
RELEASE_NAME="${RELEASE_NAME:-gratefulagents}"
TIMEOUT="${TIMEOUT:-15m}"

usage() {
  cat <<'EOF'
Install gratefulagents from published images into a local Kind cluster.

Usage:
  ./scripts/install-kind.sh

Docker must already be installed and running. The script downloads pinned Kind,
kubectl, and Helm binaries into the user's cache when compatible versions are
not already available. It does not use sudo or change the default kubeconfig.

Environment overrides:
  AGENT_SANDBOX_VERSION agent-sandbox release (default: v0.3.10)
  CHART_DIR             local Helm chart (auto-detected in a checkout)
  CLUSTER_NAME          Kind cluster name (default: gratefulagents)
  DASHBOARD_PORT        localhost dashboard port (default: 8090)
  GRATEFULAGENTS_REF    chart source ref if no local chart exists (default: v0.1.0)
  HELM_VERSION          Helm version (default: v3.18.6)
  IMAGE_REGISTRY        image registry/namespace (default: ghcr.io/gratefulagents)
  IMAGE_TAG             controller, worker, and injector tag (default: v0.1.0)
  KIND_NODE_IMAGE       pinned Kind node image
  KIND_VERSION          Kind version (default: v0.29.0)
  KUBECONFIG_FILE       dedicated kubeconfig path (default: under STATE_DIR)
  KUBECTL_VERSION       kubectl version (default: v1.33.1)
  NAMESPACE             release namespace (default: gratefulagents-system)
  RELEASE_NAME          Helm release name (default: gratefulagents)
  STATE_DIR             credentials and kubeconfig directory
  TIMEOUT               cluster/Kubernetes/Helm timeout (default: 15m)
  TOOL_DIR              directory for installer-managed CLI binaries
  VALUES_FILE           persistent private Helm values file

The cluster and its persistent volumes survive reruns. Delete them explicitly
with: kind delete cluster --name gratefulagents
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

[[ -n "${HOME:-}" ]] || die "HOME must be set"
[[ "$CLUSTER_NAME" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "CLUSTER_NAME must be a DNS label"
[[ "$NAMESPACE" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "NAMESPACE must be a DNS label"
[[ "$RELEASE_NAME" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "RELEASE_NAME must be a DNS label"
[[ "$DASHBOARD_PORT" =~ ^[0-9]+$ ]] || die "DASHBOARD_PORT must be a number"
(( ${#DASHBOARD_PORT} <= 5 )) || die "DASHBOARD_PORT must be between 1 and 65535"
DASHBOARD_PORT="$((10#$DASHBOARD_PORT))"
(( DASHBOARD_PORT >= 1 && DASHBOARD_PORT <= 65535 )) || die "DASHBOARD_PORT must be between 1 and 65535"
[[ "$IMAGE_TAG" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || die "IMAGE_TAG is not a valid container image tag"
IMAGE_REGISTRY="${IMAGE_REGISTRY%/}"
[[ -n "$IMAGE_REGISTRY" ]] || die "IMAGE_REGISTRY must not be empty"

case "$(uname -s)" in
  Darwin) TOOL_OS="darwin" ;;
  Linux) TOOL_OS="linux" ;;
  *) die "supported operating systems are macOS and Linux" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) TOOL_ARCH="amd64" ;;
  arm64|aarch64) TOOL_ARCH="arm64" ;;
  *) die "supported architectures are x86_64 and arm64" ;;
esac

STATE_DIR="${STATE_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/gratefulagents/kind}"
TOOL_DIR="${TOOL_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/gratefulagents/bin}"
VALUES_FILE="${VALUES_FILE:-$STATE_DIR/${RELEASE_NAME}-${NAMESPACE}-values.yaml}"
KUBECONFIG_FILE="${KUBECONFIG_FILE:-$STATE_DIR/${CLUSTER_NAME}-kubeconfig}"
mkdir -p "$STATE_DIR" "$TOOL_DIR"
chmod 700 "$STATE_DIR" "$TOOL_DIR"
export PATH="$TOOL_DIR:$PATH"
export KUBECONFIG="$KUBECONFIG_FILE"

TMP_DIR="$(mktemp -d)"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v openssl >/dev/null 2>&1 || die "openssl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v docker >/dev/null 2>&1 || die "Docker is required; install Docker Desktop, OrbStack, Colima, or Docker Engine"
docker info >/dev/null 2>&1 || die "Docker is not running or the current user cannot access it"

docker_resources="$(docker info --format '{{.NCPU}} {{.MemTotal}}' 2>/dev/null || true)"
if [[ "$docker_resources" =~ ^([0-9]+)[[:space:]]+([0-9]+)$ ]]; then
  (( BASH_REMATCH[1] >= 4 )) || warn "4+ CPUs are recommended for Docker; found ${BASH_REMATCH[1]}"
  # Docker Desktop reserves a little of its configured memory for the VM.
  (( BASH_REMATCH[2] >= 15 * 512 * 1024 * 1024 )) || warn "8+ GiB of memory is recommended for Docker"
fi

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "sha256sum or shasum is required to verify downloads"
  fi
}

download_verified() {
  local download_url="$1"
  local checksum_url="$2"
  local destination="$3"
  local checksum_file="$TMP_DIR/$(basename "$destination").sha256"
  local expected_checksum
  local actual_checksum
  curl --fail --silent --show-error --location "$download_url" -o "$destination"
  curl --fail --silent --show-error --location "$checksum_url" -o "$checksum_file"
  expected_checksum="$(awk 'NR == 1 {print $1}' "$checksum_file")"
  actual_checksum="$(sha256_file "$destination")"
  [[ "$expected_checksum" =~ ^[a-fA-F0-9]{64}$ ]] || die "invalid checksum downloaded from $checksum_url"
  [[ "$actual_checksum" == "$expected_checksum" ]] || die "checksum verification failed for $download_url"
}

if ! kind version 2>/dev/null | grep -Fq "$KIND_VERSION"; then
  log "Installing Kind $KIND_VERSION in $TOOL_DIR"
  kind_download="$TMP_DIR/kind"
  kind_url="https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-${TOOL_OS}-${TOOL_ARCH}"
  download_verified "$kind_url" "${kind_url}.sha256sum" "$kind_download"
  chmod 755 "$kind_download"
  mv "$kind_download" "$TOOL_DIR/kind"
  hash -r
fi

if ! kubectl version --client -o json 2>/dev/null | grep -Fq "\"gitVersion\": \"$KUBECTL_VERSION\""; then
  log "Installing kubectl $KUBECTL_VERSION in $TOOL_DIR"
  kubectl_download="$TMP_DIR/kubectl"
  kubectl_url="https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${TOOL_OS}/${TOOL_ARCH}/kubectl"
  download_verified "$kubectl_url" "${kubectl_url}.sha256" "$kubectl_download"
  chmod 755 "$kubectl_download"
  mv "$kubectl_download" "$TOOL_DIR/kubectl"
  hash -r
fi

helm_version="$(helm version --template '{{.Version}}' 2>/dev/null || true)"
if [[ "$helm_version" != "$HELM_VERSION" ]]; then
  log "Installing Helm $HELM_VERSION in $TOOL_DIR"
  helm_archive="helm-${HELM_VERSION}-${TOOL_OS}-${TOOL_ARCH}.tar.gz"
  helm_url="https://get.helm.sh/${helm_archive}"
  download_verified "$helm_url" "${helm_url}.sha256sum" "$TMP_DIR/$helm_archive"
  tar -xzf "$TMP_DIR/$helm_archive" -C "$TMP_DIR"
  chmod 755 "$TMP_DIR/${TOOL_OS}-${TOOL_ARCH}/helm"
  mv "$TMP_DIR/${TOOL_OS}-${TOOL_ARCH}/helm" "$TOOL_DIR/helm"
  hash -r
fi
[[ "$(helm version --template '{{.Version}}')" == v3.* ]] || die "Helm 3 is required"

script_path="${BASH_SOURCE[0]:-$0}"
script_dir="$(cd -- "$(dirname -- "$script_path")" && pwd)"
if [[ -z "$CHART_DIR" && -f "$script_dir/../dist/chart/Chart.yaml" ]]; then
  CHART_DIR="$(cd "$script_dir/../dist/chart" && pwd)"
fi
if [[ -z "$CHART_DIR" ]]; then
  command -v git >/dev/null 2>&1 || die "git is required when the installer is run outside a repository checkout"
  log "Fetching the gratefulagents $GRATEFULAGENTS_REF Helm chart"
  git clone --depth 1 --branch "$GRATEFULAGENTS_REF" "$GRATEFULAGENTS_REPOSITORY_URL" "$TMP_DIR/gratefulagents"
  CHART_DIR="$TMP_DIR/gratefulagents/dist/chart"
else
  CHART_DIR="$(cd "$CHART_DIR" && pwd)"
fi
[[ -f "$CHART_DIR/Chart.yaml" ]] || die "Helm chart not found at $CHART_DIR"

if kind get clusters 2>/dev/null | grep -Fxq "$CLUSTER_NAME"; then
  log "Keeping existing Kind cluster $CLUSTER_NAME"
  kind get kubeconfig --name "$CLUSTER_NAME" >"$KUBECONFIG_FILE"
  cluster_state="reused"
else
  log "Creating Kind cluster $CLUSTER_NAME"
  cat >"$TMP_DIR/kind-config.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30090
        hostPort: $DASHBOARD_PORT
        listenAddress: "127.0.0.1"
        protocol: TCP
EOF
  kind create cluster \
    --name "$CLUSTER_NAME" \
    --image "$KIND_NODE_IMAGE" \
    --config "$TMP_DIR/kind-config.yaml" \
    --kubeconfig "$KUBECONFIG_FILE" \
    --wait "$TIMEOUT"
  cluster_state="created"
fi
chmod 600 "$KUBECONFIG_FILE"

kubectl cluster-info >/dev/null
kubectl wait --for=condition=Ready nodes --all --timeout="$TIMEOUT"
kubectl get storageclass standard >/dev/null 2>&1 || die "the Kind standard StorageClass is missing"

log "Installing agent-sandbox $AGENT_SANDBOX_VERSION"
manifest_base="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}"
curl --fail --silent --show-error --location "$manifest_base/manifest.yaml" -o "$TMP_DIR/agent-sandbox-manifest.yaml"
curl --fail --silent --show-error --location "$manifest_base/extensions.yaml" -o "$TMP_DIR/agent-sandbox-extensions.yaml"
kubectl apply -f "$TMP_DIR/agent-sandbox-manifest.yaml"
kubectl apply -f "$TMP_DIR/agent-sandbox-extensions.yaml"
kubectl -n agent-sandbox-system wait --for=condition=Available deployment --all --timeout="$TIMEOUT"

if [[ ! -f "$VALUES_FILE" ]] && helm status "$RELEASE_NAME" --namespace "$NAMESPACE" >/dev/null 2>&1; then
  die "release $NAMESPACE/$RELEASE_NAME exists but $VALUES_FILE is missing; restore it before upgrading so persistent-service credentials are not rotated"
fi
if [[ ! -f "$VALUES_FILE" ]]; then
  log "Generating persistent local-service credentials"
  postgres_password="$(openssl rand -hex 24)"
  minio_password="$(openssl rand -hex 24)"
  umask 077
  cat >"$VALUES_FILE" <<EOF
# Generated by scripts/install-kind.sh. Keep this file private while the cluster exists.
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
else
  log "Reusing installer values from $VALUES_FILE"
fi

manager_image_repository="$IMAGE_REGISTRY/controller"
worker_image="$IMAGE_REGISTRY/worker:$IMAGE_TAG"
injector_image="$IMAGE_REGISTRY/injector:$IMAGE_TAG"

log "Installing gratefulagents $IMAGE_TAG from $IMAGE_REGISTRY"
helm lint "$CHART_DIR" --values "$VALUES_FILE" \
  --set dashboard.service.type=NodePort \
  --set dashboard.service.nodePort=30090 \
  --set-string "manager.image.repository=$manager_image_repository" \
  --set-string "manager.image.tag=$IMAGE_TAG" \
  --set manager.image.pullPolicy=IfNotPresent \
  --set-string "agentImages.worker=$worker_image" \
  --set-string "agentImages.injector=$injector_image"
helm upgrade --install "$RELEASE_NAME" "$CHART_DIR" \
  --namespace "$NAMESPACE" --create-namespace \
  --values "$VALUES_FILE" \
  --set dashboard.service.type=NodePort \
  --set dashboard.service.nodePort=30090 \
  --set-string "manager.image.repository=$manager_image_repository" \
  --set-string "manager.image.tag=$IMAGE_TAG" \
  --set manager.image.pullPolicy=IfNotPresent \
  --set-string "agentImages.worker=$worker_image" \
  --set-string "agentImages.injector=$injector_image" \
  --atomic --wait --wait-for-jobs --timeout "$TIMEOUT" --history-max 10

manager_deployment="$(kubectl -n "$NAMESPACE" get deployment \
  -l "app.kubernetes.io/instance=$RELEASE_NAME,control-plane=controller-manager" \
  -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$manager_deployment" ]] || die "controller-manager Deployment was not found"
kubectl -n "$NAMESPACE" rollout status "deployment/$manager_deployment" --timeout="$TIMEOUT"
kubectl -n "$NAMESPACE" get secret gratefulagents-admin-credentials >/dev/null

dashboard_service="$(kubectl -n "$NAMESPACE" get service \
  -l "app.kubernetes.io/instance=$RELEASE_NAME" \
  -o jsonpath='{.items[?(@.spec.ports[0].name=="dashboard")].metadata.name}')"
[[ -n "$dashboard_service" ]] || die "dashboard Service was not found"

dashboard_host_port="$(docker inspect --format '{{with (index .NetworkSettings.Ports "30090/tcp")}}{{(index . 0).HostPort}}{{end}}' \
  "${CLUSTER_NAME}-control-plane" 2>/dev/null || true)"
if [[ -n "$dashboard_host_port" ]]; then
  dashboard_access="http://127.0.0.1:$dashboard_host_port"
  if ! curl --fail --silent --show-error --max-time 10 "$dashboard_access/" >/dev/null; then
    warn "the dashboard is installed but did not answer at $dashboard_access yet"
  fi
else
  dashboard_access="Run the port-forward command below, then open http://127.0.0.1:$DASHBOARD_PORT"
fi

kind_bin="$(command -v kind)"
kubectl_bin="$(command -v kubectl)"
cat <<EOF

Installation complete.

Dashboard: $dashboard_access
Username:  admin
Password:  KUBECONFIG="$KUBECONFIG_FILE" "$kubectl_bin" -n "$NAMESPACE" get secret gratefulagents-admin-credentials \\
             -o go-template='{{.data.password | base64decode}}{{"\\n"}}'

Cluster:    $CLUSTER_NAME ($cluster_state)
Kubeconfig: $KUBECONFIG_FILE
Values:     $VALUES_FILE
Images:     $IMAGE_REGISTRY/{controller,worker,injector}:$IMAGE_TAG

Use the installer-managed tools in a new shell:
  export PATH="$TOOL_DIR:\$PATH"
  export KUBECONFIG="$KUBECONFIG_FILE"

If the dashboard port was not mapped by a pre-existing cluster:
  "$kubectl_bin" -n "$NAMESPACE" port-forward service/$dashboard_service "$DASHBOARD_PORT:8090"

Delete the local cluster and all of its data:
  "$kind_bin" delete cluster --name "$CLUSTER_NAME"
EOF
