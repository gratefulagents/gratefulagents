#!/usr/bin/env bash
# Install maintainer dependencies for one stage of the release pipeline.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ANDROID_API_LEVEL="${ANDROID_API_LEVEL:-36}"
ANDROID_NDK_VERSION="${ANDROID_NDK_VERSION:-27.3.13750724}"
PNPM_VERSION="${PNPM_VERSION:-10.25.0}"
RELEASE_STAGE="${1:-all}"

log() { printf '\n==> %s\n' "$*"; }
die() { printf 'release dependencies: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

install_pnpm() {
  local installed_version=""
  if command -v pnpm >/dev/null 2>&1; then
    installed_version="$(pnpm --version)"
  fi
  if [[ "${installed_version}" == "${PNPM_VERSION}" ]]; then
    return
  fi

  if command -v corepack >/dev/null 2>&1; then
    corepack enable
    corepack prepare "pnpm@${PNPM_VERSION}" --activate
  else
    # Corepack is no longer bundled with newer Node.js releases. npm remains
    # available in Homebrew's Node package, so use it to install the pinned CLI.
    require_command npm
    npm install --global "pnpm@${PNPM_VERSION}"
  fi

  require_command pnpm
  installed_version="$(pnpm --version)"
  [[ "${installed_version}" == "${PNPM_VERSION}" ]] || \
    die "expected pnpm ${PNPM_VERSION}, found ${installed_version}"
}

install_linux() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is missing"
  # shellcheck disable=SC1091
  source /etc/os-release
  [[ "${ID:-}" == "debian" ]] || die "Linux release builds require Debian (found: ${ID:-unknown})"

  if [[ $EUID -eq 0 ]]; then
    ROOT=()
  else
    require_command sudo
    ROOT=(sudo)
  fi

  log "Installing Debian release tools from APT"
  "${ROOT[@]}" env DEBIAN_FRONTEND=noninteractive apt-get update
  "${ROOT[@]}" env DEBIAN_FRONTEND=noninteractive apt-get install -y \
    ca-certificates curl gh git gnupg jq make openssl tar util-linux

  "${REPO_ROOT}/scripts/install-docker-engine.sh"
}

install_macos() {
  require_command brew
  require_command codesign
  require_command xcrun

  log "Installing macOS desktop release tools"
  brew install gh jq node rustup-init

  if ! command -v rustup >/dev/null 2>&1; then
    rustup-init -y --default-toolchain stable
    export PATH="${HOME}/.cargo/bin:${PATH}"
  fi
  install_pnpm

  rustup default stable
  rustup target add aarch64-apple-darwin

  if [[ "${RELEASE_STAGE}" == "macos" ]]; then
    return
  fi

  [[ -d /Applications/Xcode.app ]] || die "install and launch the full Xcode app before preparing iOS builds"
  require_command xcodebuild

  log "Installing optional iOS and Android release tools"
  brew install cocoapods temurin
  brew install --cask android-commandlinetools

  export JAVA_HOME="${JAVA_HOME:-$(brew --prefix temurin)/libexec/openjdk.jdk/Contents/Home}"
  export ANDROID_HOME="${ANDROID_HOME:-$(brew --prefix)/share/android-commandlinetools}"
  local sdkmanager="${ANDROID_HOME}/cmdline-tools/latest/bin/sdkmanager"
  [[ -x "${sdkmanager}" ]] || die "Android command-line tools were not installed at ${sdkmanager}"

  rustup target add \
    aarch64-apple-ios \
    x86_64-apple-ios \
    aarch64-apple-ios-sim \
    aarch64-linux-android \
    armv7-linux-androideabi \
    i686-linux-android \
    x86_64-linux-android
  yes | "${sdkmanager}" --sdk_root="${ANDROID_HOME}" --licenses >/dev/null
  "${sdkmanager}" --sdk_root="${ANDROID_HOME}" \
    "platform-tools" \
    "platforms;android-${ANDROID_API_LEVEL}" \
    "build-tools;${ANDROID_API_LEVEL}.0.0" \
    "ndk;${ANDROID_NDK_VERSION}"
}

case "${RELEASE_STAGE}" in
  linux|macos|all) ;;
  *) die "stage must be linux, macos, or all (got ${RELEASE_STAGE})" ;;
esac

case "$(uname -s)" in
  Linux) install_linux ;;
  Darwin) install_macos ;;
  *) die "supported release hosts are Debian Linux and macOS" ;;
esac
