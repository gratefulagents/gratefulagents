#!/usr/bin/env bash
# Install Docker Engine from Docker's signed APT repository.
set -Eeuo pipefail

log() { printf '\n==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

if [[ $EUID -eq 0 ]]; then
  ROOT=()
else
  command -v sudo >/dev/null 2>&1 || die "sudo is required when not running as root"
  ROOT=(sudo)
fi

[[ -r /etc/os-release ]] || die "/etc/os-release is missing"
# shellcheck disable=SC1091
source /etc/os-release
case "${ID:-}" in
  debian|ubuntu) ;;
  *) die "supported operating systems are Debian and Ubuntu (found: ${ID:-unknown})" ;;
esac
[[ -n "${VERSION_CODENAME:-}" ]] || die "VERSION_CODENAME is required to configure Docker's APT repository"

log "Configuring Docker's official APT repository"
"${ROOT[@]}" install -m 0755 -d /etc/apt/keyrings
"${ROOT[@]}" curl -fsSL "https://download.docker.com/linux/${ID}/gpg" \
  -o /etc/apt/keyrings/docker.asc
"${ROOT[@]}" chmod a+r /etc/apt/keyrings/docker.asc
"${ROOT[@]}" tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/${ID}
Suites: ${VERSION_CODENAME}
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
"${ROOT[@]}" env DEBIAN_FRONTEND=noninteractive apt-get update

if ! dpkg-query -W -f='${db:Status-Status}' docker-ce 2>/dev/null | grep -qx installed; then
  log "Removing Docker packages that conflict with Docker Engine"
  "${ROOT[@]}" env DEBIAN_FRONTEND=noninteractive apt-get remove -y \
    docker.io docker-compose docker-doc podman-docker containerd runc || true
fi

log "Installing Docker Engine from Docker's APT repository"
"${ROOT[@]}" env DEBIAN_FRONTEND=noninteractive apt-get install -y \
  docker-ce \
  docker-ce-cli \
  containerd.io \
  docker-buildx-plugin \
  docker-compose-plugin
"${ROOT[@]}" systemctl enable --now docker

docker_user=""
if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
  docker_user="${SUDO_USER}"
elif [[ $EUID -ne 0 ]]; then
  docker_user="$(id -un)"
fi
if [[ -n "${docker_user}" ]] && getent group docker >/dev/null 2>&1; then
  "${ROOT[@]}" usermod -aG docker "${docker_user}"
  printf 'Docker access was granted to %s; sign out and back in for it to take effect.\n' "${docker_user}"
fi
