#!/usr/bin/env bash
# Install the latest gratefulagents desktop release for this OS and CPU.
set -Eeuo pipefail

REPOSITORY="${GRATEFULAGENTS_REPOSITORY:-gratefulagents/gratefulagents}"
GITHUB_TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
INSTALL_DIR="${INSTALL_DIR:-}"

TMP_DIR=""
MOUNT_POINT=""
STAGED_APP=""
BACKUP_APP=""
DESTINATION_APP=""

log() { printf '\n==> %s\n' "$*" >&2; }
die() { printf 'install-app: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

cleanup() {
  if [[ -n "${MOUNT_POINT}" && -d "${MOUNT_POINT}" ]]; then
    hdiutil detach "${MOUNT_POINT}" -quiet >/dev/null 2>&1 || true
  fi
  if [[ -n "${STAGED_APP}" && -e "${STAGED_APP}" ]]; then
    rm -rf "${STAGED_APP}"
  fi
  if [[ -n "${BACKUP_APP}" && -e "${BACKUP_APP}" ]]; then
    if [[ -n "${DESTINATION_APP}" && ! -e "${DESTINATION_APP}" ]]; then
      mv "${BACKUP_APP}" "${DESTINATION_APP}" || true
    else
      rm -rf "${BACKUP_APP}"
    fi
  fi
  if [[ -n "${TMP_DIR}" && -d "${TMP_DIR}" ]]; then
    rm -rf "${TMP_DIR}"
  fi
}
trap cleanup EXIT

github_curl() {
  if [[ -n "${GITHUB_TOKEN}" ]]; then
    curl --fail --silent --show-error --location \
      -H "Authorization: Bearer ${GITHUB_TOKEN}" \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      -H 'User-Agent: gratefulagents-installer' \
      "$@"
  else
    curl --fail --silent --show-error --location \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      -H 'User-Agent: gratefulagents-installer' \
      "$@"
  fi
}

select_asset_url() {
  local os="$1"
  local arch="$2"
  local asset_urls="$3"
  local url
  local name
  local fallback=""
  local fallback_count=0

  while IFS= read -r url; do
    [[ -n "${url}" ]] || continue
    name="$(basename "${url%%\?*}" | tr '[:upper:]' '[:lower:]')"

    case "${os}/${arch}/${name}" in
      macos/arm64/*.dmg)
        fallback="${url}"
        fallback_count=$((fallback_count + 1))
        case "${name}" in
          *aarch64*|*arm64*) printf '%s\n' "${url}"; return ;;
        esac
        ;;
      linux/amd64/*.appimage)
        case "${name}" in
          *amd64*|*x86_64*|*x86-64*) printf '%s\n' "${url}"; return ;;
        esac
        ;;
      linux/arm64/*.appimage)
        case "${name}" in
          *aarch64*|*arm64*) printf '%s\n' "${url}"; return ;;
        esac
        ;;
    esac
  done <<< "${asset_urls}"

  # The macOS release currently has a single Apple Silicon DMG. Accept its
  # name even if a future bundler omits the architecture suffix.
  if [[ "${os}/${arch}" == "macos/arm64" && "${fallback_count}" == "1" ]]; then
    printf '%s\n' "${fallback}"
    return
  fi
  return 1
}

install_macos() {
  local asset_url="$1"
  local dmg="${TMP_DIR}/gratefulagents.dmg"
  local attach_output
  local source_app
  local app_name
  local destination

  require_command hdiutil
  require_command ditto
  require_command xattr

  if [[ -z "${INSTALL_DIR}" ]]; then
    if [[ -d /Applications && -w /Applications ]]; then
      INSTALL_DIR=/Applications
    else
      INSTALL_DIR="${HOME}/Applications"
    fi
  fi
  mkdir -p "${INSTALL_DIR}"
  [[ -w "${INSTALL_DIR}" ]] || die "${INSTALL_DIR} is not writable; set INSTALL_DIR to a writable Applications directory"

  log "Downloading the latest macOS ARM64 release"
  github_curl --output "${dmg}" "${asset_url}"

  log "Mounting $(basename "${dmg}")"
  attach_output="$(hdiutil attach -nobrowse -readonly "${dmg}")"
  MOUNT_POINT="$(printf '%s\n' "${attach_output}" | awk -F '\t' '$NF ~ /^\/Volumes\// { print $NF; exit }')"
  [[ -n "${MOUNT_POINT}" && -d "${MOUNT_POINT}" ]] || die "could not determine the mounted DMG path"

  source_app="$(find "${MOUNT_POINT}" -maxdepth 2 -type d -name '*.app' -print -quit)"
  [[ -n "${source_app}" ]] || die "the DMG does not contain an application bundle"
  app_name="$(basename "${source_app}")"
  destination="${INSTALL_DIR}/${app_name}"
  STAGED_APP="${INSTALL_DIR}/.${app_name}.new.$$"
  BACKUP_APP="${INSTALL_DIR}/.${app_name}.previous.$$"
  DESTINATION_APP="${destination}"

  log "Installing ${app_name} in ${INSTALL_DIR}"
  rm -rf "${STAGED_APP}" "${BACKUP_APP}"
  ditto "${source_app}" "${STAGED_APP}"
  xattr -cr "${STAGED_APP}"
  if [[ -e "${destination}" ]]; then
    mv "${destination}" "${BACKUP_APP}"
  fi
  if mv "${STAGED_APP}" "${destination}"; then
    STAGED_APP=""
    rm -rf "${BACKUP_APP}"
    BACKUP_APP=""
  else
    if [[ -e "${BACKUP_APP}" ]]; then
      mv "${BACKUP_APP}" "${destination}"
      BACKUP_APP=""
    fi
    die "could not replace ${destination}"
  fi

  printf '\nInstalled: %s\n' "${destination}"
  printf 'Quarantine attributes cleared with: xattr -cr %q\n' "${destination}"
}

install_linux() {
  local asset_url="$1"
  local appimage="${TMP_DIR}/gratefulagents.AppImage"
  local destination

  if [[ -z "${INSTALL_DIR}" ]]; then
    INSTALL_DIR="${XDG_BIN_HOME:-${HOME}/.local/bin}"
  fi
  mkdir -p "${INSTALL_DIR}"
  [[ -w "${INSTALL_DIR}" ]] || die "${INSTALL_DIR} is not writable; set INSTALL_DIR to a writable bin directory"
  destination="${INSTALL_DIR}/gratefulagents"

  log "Downloading the latest Linux ${ARCH} AppImage"
  github_curl --output "${appimage}" "${asset_url}"
  chmod 0755 "${appimage}"
  mv "${appimage}" "${destination}"

  printf '\nInstalled: %s\n' "${destination}"
  case ":${PATH}:" in
    *:"${INSTALL_DIR}":*) ;;
    *) printf 'Add %s to PATH to run gratefulagents from a shell.\n' "${INSTALL_DIR}" ;;
  esac
}

main() {
  local release_json
  local asset_urls
  local asset_url

  require_command curl
  require_command sed
  require_command uname
  [[ -n "${HOME:-}" ]] || die "HOME must be set"
  [[ "${REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "invalid repository: ${REPOSITORY}"

  case "$(uname -s)" in
    Darwin) OS=macos ;;
    Linux) OS=linux ;;
    *) die "supported operating systems are macOS and Linux" ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) ARCH=arm64 ;;
    x86_64|amd64) ARCH=amd64 ;;
    *) die "supported CPU architectures are arm64 and amd64" ;;
  esac
  if [[ "${OS}/${ARCH}" == "macos/amd64" ]]; then
    die "the macOS desktop release is currently available only for Apple Silicon"
  fi

  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gratefulagents-app.XXXXXX")"

  log "Finding the latest ${REPOSITORY} release"
  release_json="$(github_curl "https://api.github.com/repos/${REPOSITORY}/releases/latest")" || \
    die "could not read the latest release; set GITHUB_TOKEN if the repository is private"
  asset_urls="$(printf '%s\n' "${release_json}" | sed -n 's/.*"browser_download_url":[[:space:]]*"\([^"]*\)".*/\1/p')"
  [[ -n "${asset_urls}" ]] || die "the latest release has no downloadable assets"

  if ! asset_url="$(select_asset_url "${OS}" "${ARCH}" "${asset_urls}")"; then
    die "the latest release has no desktop asset for ${OS}/${ARCH}"
  fi

  case "${OS}" in
    macos) install_macos "${asset_url}" ;;
    linux) install_linux "${asset_url}" ;;
  esac
}

main "$@"
