#!/usr/bin/env bash
# Install the Debian/Ubuntu host tools required by the k3s commands.
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

log "Installing host prerequisites from APT"
"${ROOT[@]}" env DEBIAN_FRONTEND=noninteractive apt-get update
"${ROOT[@]}" env DEBIAN_FRONTEND=noninteractive apt-get install -y \
  ca-certificates \
  curl \
  git \
  gnupg \
  make \
  openssl \
  util-linux

"$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/install-docker-engine.sh"
