#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${PLATFORM_DIR}/.." && pwd)"

RELEASE_REPO="${RELEASE_REPO:-gratefulagents/gratefulagents}"
RELEASE_REMOTE="${RELEASE_REMOTE:-origin}"
RELEASE_DRY_RUN="${RELEASE_DRY_RUN:-0}"
REPLACE_RELEASE="${REPLACE_RELEASE:-0}"
RELEASE_STAGE="${1:-}"

COMMIT_SHA="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
SHORT_SHA="${COMMIT_SHA:0:7}"
COMMIT_COUNT="$(git -C "${REPO_ROOT}" rev-list --count HEAD)"
TAG_NAME="${RELEASE_TAG:-build-${SHORT_SHA}}"
APP_VERSION="${APP_VERSION:-0.1.${COMMIT_COUNT}}"
DIST_DIR="${RELEASE_DIST_DIR:-${PLATFORM_DIR}/release-dist/${TAG_NAME}}"
LINUX_IMAGE="${RELEASE_LINUX_IMAGE:-gratefulagents-tauri-release:ubuntu22-amd64}"

WORK_DIR=""
RELEASE_IS_DRAFT=0

log() {
  printf '\n==> %s\n' "$*"
}

die() {
  printf 'release: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

cleanup() {
  if [[ -n "${WORK_DIR}" && -d "${WORK_DIR}" ]]; then
    rm -rf "${WORK_DIR}"
  fi
}

on_error() {
  local status=$?
  if [[ "${RELEASE_IS_DRAFT}" == "1" ]]; then
    printf '\nRelease build failed. Draft %s remains unpublished for inspection.\n' "${TAG_NAME}" >&2
  fi
  exit "${status}"
}

trap cleanup EXIT
trap on_error ERR

write_linux_configs() {
  local updater_config="$1"
  local android_config="$2"

  jq -n \
    --arg version "${APP_VERSION}" \
    '{
      version: $version,
      bundle: {
        createUpdaterArtifacts: true,
        targets: ["deb", "rpm", "appimage"]
      }
    }' > "${updater_config}"

  jq -n \
    --arg version "${APP_VERSION}" \
    --argjson version_code "${COMMIT_COUNT}" \
    '{ version: $version, bundle: { android: { versionCode: $version_code } } }' \
    > "${android_config}"
}

write_macos_updater_config() {
  local updater_config="$1"

  jq -n \
    --arg version "${APP_VERSION}" \
    '{
      version: $version,
      bundle: {
        createUpdaterArtifacts: true,
        targets: ["app", "dmg"]
      }
    }' > "${updater_config}"
}

archive_source() {
  local destination="$1"
  mkdir -p "${destination}"
  git -C "${REPO_ROOT}" archive --format=tar HEAD | tar -xf - -C "${destination}"
}

copy_matching_artifacts() {
  local source_root="$1"
  shift
  local file
  local pattern

  while IFS= read -r -d '' file; do
    for pattern in "$@"; do
      if [[ "$(basename "${file}")" == ${pattern} ]]; then
        cp "${file}" "${DIST_DIR}/"
        break
      fi
    done
  done < <(find "${source_root}" -type f -print0)
}

require_artifact() {
  local pattern="$1"
  local description="$2"
  local found
  found="$(find "${DIST_DIR}" -maxdepth 1 -type f -name "${pattern}" -print -quit)"
  [[ -n "${found}" ]] || die "missing ${description} (${pattern})"
}

load_signing_key() {
  if [[ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" && -z "${TAURI_SIGNING_PRIVATE_KEY_PATH:-}" ]]; then
    [[ -n "${HOME:-}" ]] || die "HOME is not set; set TAURI_SIGNING_PRIVATE_KEY or TAURI_SIGNING_PRIVATE_KEY_PATH"
    TAURI_SIGNING_PRIVATE_KEY_PATH="${HOME}/.tauri/gratefulagents.key"
  fi
  if [[ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" && -n "${TAURI_SIGNING_PRIVATE_KEY_PATH:-}" ]]; then
    [[ -f "${TAURI_SIGNING_PRIVATE_KEY_PATH}" ]] || die "signing key file not found: ${TAURI_SIGNING_PRIVATE_KEY_PATH}"
    TAURI_SIGNING_PRIVATE_KEY="$(<"${TAURI_SIGNING_PRIVATE_KEY_PATH}")"
    export TAURI_SIGNING_PRIVATE_KEY
  fi
  [[ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]] || die "set TAURI_SIGNING_PRIVATE_KEY or TAURI_SIGNING_PRIVATE_KEY_PATH"
  export TAURI_SIGNING_PRIVATE_KEY_PASSWORD="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"
}

ensure_tag_and_release() {
  local existing_tag_sha=""
  local existing_release=""
  local existing_release_is_draft=""
  local existing_release_info=""
  local notes_file="$1"

  # Query the ref directly. The commits endpoint returns a JSON error body for
  # a missing tag, which must not be mistaken for an existing commit SHA.
  if ! existing_tag_sha="$(gh api "repos/${RELEASE_REPO}/git/ref/tags/${TAG_NAME}" --jq .object.sha 2>/dev/null)"; then
    existing_tag_sha=""
  fi
  if existing_release_info="$(gh release view "${TAG_NAME}" \
    --repo "${RELEASE_REPO}" \
    --json tagName,isDraft \
    --jq '[.tagName, (.isDraft | tostring)] | @tsv' 2>/dev/null)"; then
    IFS=$'\t' read -r existing_release existing_release_is_draft <<< "${existing_release_info}"
  fi

  if [[ -n "${existing_tag_sha}" && "${existing_tag_sha}" != "${COMMIT_SHA}" ]]; then
    die "remote tag ${TAG_NAME} points at ${existing_tag_sha}, not ${COMMIT_SHA}"
  fi
  if [[ -n "${existing_release}" && "${REPLACE_RELEASE}" == "1" ]]; then
    log "Deleting existing release ${TAG_NAME}; its tag is retained"
    gh release delete "${TAG_NAME}" --repo "${RELEASE_REPO}" --yes
    existing_release=""
    existing_release_is_draft=""
  fi
  if [[ -z "${existing_tag_sha}" ]]; then
    log "Creating GitHub tag ${TAG_NAME} at ${COMMIT_SHA}"
    gh api --method POST "repos/${RELEASE_REPO}/git/refs" \
      -f ref="refs/tags/${TAG_NAME}" \
      -f sha="${COMMIT_SHA}" \
      --jq .ref >/dev/null
  fi

  if [[ -n "${existing_release}" ]]; then
    if [[ "${existing_release_is_draft}" == "true" ]]; then
      RELEASE_IS_DRAFT=1
      log "Reusing draft GitHub release ${TAG_NAME}"
    else
      RELEASE_IS_DRAFT=0
      log "Reusing published GitHub release ${TAG_NAME}"
    fi
    return
  fi

  log "Creating draft GitHub release ${TAG_NAME}"
  gh release create "${TAG_NAME}" \
    --repo "${RELEASE_REPO}" \
    --verify-tag \
    --draft \
    --title "gratefulagents build ${SHORT_SHA}" \
    --notes-file "${notes_file}"
  RELEASE_IS_DRAFT=1
}

build_linux() {
  local source_dir="$1"
  local updater_config="$2"
  local android_config="$3"
  local linux_bundle_root="${source_dir}/platform-app/tauri/src-tauri/target/release/bundle"
  local tauri_dir="${source_dir}/platform-app/tauri"
  local apk

  log "Building Linux x86_64 release container"
  docker build \
    --platform linux/amd64 \
    --file "${PLATFORM_DIR}/tauri/release-linux.Dockerfile" \
    --tag "${LINUX_IMAGE}" \
    "${REPO_ROOT}"

  log "Building signed Linux x86_64 bundles in Docker"
  docker run --rm \
    --platform linux/amd64 \
    --env CI=true \
    --env VITE_APP_VERSION="${APP_VERSION}" \
    --env VITE_BUILD_COMMIT="${COMMIT_SHA}" \
    --env TAURI_SIGNING_PRIVATE_KEY \
    --env TAURI_SIGNING_PRIVATE_KEY_PASSWORD \
    --volume "${source_dir}:/workspace" \
    --volume "${updater_config}:/tmp/updater.conf.json:ro" \
    --volume "${android_config}:/tmp/android.conf.json:ro" \
    --volume gratefulagents-release-cargo-registry:/root/.cargo/registry \
    --volume gratefulagents-release-cargo-git:/root/.cargo/git \
    --workdir /workspace/platform-app \
    "${LINUX_IMAGE}" \
    bash -lc 'pnpm install --frozen-lockfile && cd tauri && pnpm tauri build --config /tmp/updater.conf.json --ci && pnpm tauri android init --ci && pnpm tauri icon app-icon.png && pnpm tauri android build --debug --apk --target aarch64 --config /tmp/android.conf.json --ci'

  copy_matching_artifacts "${linux_bundle_root}" '*.deb' '*.rpm' '*.AppImage' '*.AppImage.sig'
  apk="$(find "${tauri_dir}/src-tauri/gen/android" -type f -name '*.apk' -path '*debug*' -print -quit)"
  [[ -n "${apk}" ]] || die "Android build completed without producing a debug APK"
  cp "${apk}" "${DIST_DIR}/gratefulagents-${TAG_NAME}-android-arm64-debug.apk"
}

build_macos() {
  local source_dir="$1"
  local updater_config="$2"
  local tauri_dir="${source_dir}/platform-app/tauri"
  local mac_bundle_root="${tauri_dir}/src-tauri/target/aarch64-apple-darwin/release/bundle"

  log "Installing macOS frontend dependencies"
  (cd "${source_dir}/platform-app" && pnpm install --frozen-lockfile)

  log "Building signed macOS ARM64 bundles"
  rustup target add aarch64-apple-darwin
  (
    cd "${tauri_dir}"
    CI=true \
    VITE_APP_VERSION="${APP_VERSION}" \
    VITE_BUILD_COMMIT="${COMMIT_SHA}" \
    APPLE_SIGNING_IDENTITY="${APPLE_SIGNING_IDENTITY:--}" \
    pnpm tauri build \
      --target aarch64-apple-darwin \
      --config "${updater_config}" \
      --ci
  )
  copy_matching_artifacts "${mac_bundle_root}" '*.dmg' '*.app.tar.gz' '*.app.tar.gz.sig'
}

upload_artifacts() {
  local artifacts=()
  local file

  while IFS= read -r -d '' file; do
    artifacts+=("${file}")
  done < <(find "${DIST_DIR}" -maxdepth 1 -type f ! -name 'latest.json' -print0)

  [[ "${#artifacts[@]}" -gt 0 ]] || die "no release artifacts found in ${DIST_DIR}"
  log "Uploading ${#artifacts[@]} artifacts to release ${TAG_NAME}"
  gh release upload "${TAG_NAME}" "${artifacts[@]}" --repo "${RELEASE_REPO}" --clobber
}

build_updater_manifest() {
  local assets_json="$1"
  local platforms_json="$2"
  local latest_json="$3"
  shift 3
  local release_id
  local release_api_url
  local sig_name
  local asset_name
  local platform_key
  local asset_download_url
  local asset_id
  local sig_id
  local signature

  # GitHub's REST lookup by tag can return 404 for a draft release. `gh release
  # view` resolves the draft correctly; its API URL contains the REST numeric ID
  # needed for the asset endpoints below.
  release_api_url="$(gh release view "${TAG_NAME}" --repo "${RELEASE_REPO}" --json apiUrl --jq .apiUrl)"
  release_id="${release_api_url##*/}"
  [[ "${release_id}" =~ ^[0-9]+$ ]] || die "could not determine the REST release ID for ${TAG_NAME}"
  gh api "repos/${RELEASE_REPO}/releases/${release_id}/assets?per_page=100" > "${assets_json}"
  printf '{}\n' > "${platforms_json}"

  while IFS= read -r sig_name; do
    asset_name="${sig_name%.sig}"
    case "${asset_name}" in
      *.app.tar.gz) platform_key="darwin-aarch64" ;;
      *.AppImage) platform_key="linux-x86_64" ;;
      *) continue ;;
    esac

    asset_id="$(jq -r --arg name "${asset_name}" '.[] | select(.name == $name) | .id' "${assets_json}")"
    sig_id="$(jq -r --arg name "${sig_name}" '.[] | select(.name == $name) | .id' "${assets_json}")"
    [[ -n "${asset_id}" && "${asset_id}" != "null" ]] || die "missing updater artifact for ${sig_name}"
    [[ -n "${sig_id}" && "${sig_id}" != "null" ]] || die "missing signature asset ${sig_name}"

    # A draft release's browser_download_url uses a temporary `untagged-*`
    # identifier that becomes a 404 after publication. The tag URL is stable
    # before and after the release is published.
    asset_download_url="https://github.com/${RELEASE_REPO}/releases/download/${TAG_NAME}/${asset_name}"
    signature="$(gh api \
      -H 'Accept: application/octet-stream' \
      "repos/${RELEASE_REPO}/releases/assets/${sig_id}")"
    jq \
      --arg key "${platform_key}" \
      --arg url "${asset_download_url}" \
      --arg signature "${signature}" \
      '.[$key] = { signature: $signature, url: $url }' \
      "${platforms_json}" > "${platforms_json}.tmp"
    mv "${platforms_json}.tmp" "${platforms_json}"
    printf 'manifest: %s -> %s\n' "${platform_key}" "${asset_name}"
  done < <(jq -r '.[] | select(.name | endswith(".sig")) | .name' "${assets_json}")

  for platform_key in "$@"; do
    if [[ "$(jq -r --arg key "${platform_key}" '.[$key] // empty | .url' "${platforms_json}")" == "" ]]; then
      die "missing updater artifact for ${platform_key}"
    fi
  done

  jq -n \
    --arg version "${APP_VERSION}" \
    --arg notes "gratefulagents build ${SHORT_SHA} (${RELEASE_REPO}@${COMMIT_SHA})" \
    --arg pub_date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --slurpfile platforms "${platforms_json}" \
    '{ version: $version, notes: $notes, pub_date: $pub_date, platforms: $platforms[0] }' \
    > "${latest_json}"
}

upload_updater_manifest() {
  local latest_json="$1"

  log "Uploading updater manifest"
  cp "${latest_json}" "${DIST_DIR}/latest.json"
  gh release upload "${TAG_NAME}" "${DIST_DIR}/latest.json" \
    --repo "${RELEASE_REPO}" \
    --clobber
}

publish_release() {
  log "Publishing ${TAG_NAME} as the latest release"
  gh release edit "${TAG_NAME}" \
    --repo "${RELEASE_REPO}" \
    --draft=false \
    --latest
  RELEASE_IS_DRAFT=0
  gh release view "${TAG_NAME}" --repo "${RELEASE_REPO}" --json url --jq .url
}

main() {
  cd "${REPO_ROOT}"

  require_command git
  [[ $# -eq 1 ]] || die "usage: $0 <linux|macos>"
  case "${RELEASE_STAGE}" in
    linux|macos) ;;
    *) die "stage must be linux or macos" ;;
  esac
  [[ "${RELEASE_REPO}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "RELEASE_REPO must be owner/name (got ${RELEASE_REPO})"
  [[ "${TAG_NAME}" =~ ^[A-Za-z0-9._-]+$ ]] || die "release tag contains unsupported characters: ${TAG_NAME}"
  [[ "${APP_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]] || die "APP_VERSION must be semver (got ${APP_VERSION})"
  case "${DIST_DIR}" in
    "${PLATFORM_DIR}"/release-dist/*) ;;
    *) die "RELEASE_DIST_DIR must stay under ${PLATFORM_DIR}/release-dist" ;;
  esac

  printf 'Release repo:  %s\n' "${RELEASE_REPO}"
  printf 'Commit:        %s\n' "${COMMIT_SHA}"
  printf 'Tag:           %s\n' "${TAG_NAME}"
  printf 'App version:   %s\n' "${APP_VERSION}"
  printf 'Stage:         %s\n' "${RELEASE_STAGE}"
  printf 'Artifacts:     %s\n' "${DIST_DIR}"

  if [[ "${RELEASE_DRY_RUN}" == "1" ]]; then
    if [[ -n "$(git status --porcelain)" ]]; then
      printf 'Dry run note: working tree is dirty; a real release would stop.\n'
    fi
    printf 'Dry run only: no builds, tags, releases, or uploads were performed.\n'
    return
  fi

  [[ -z "$(git status --porcelain)" ]] || die "working tree must be clean so artifacts exactly match ${COMMIT_SHA}"

  case "${RELEASE_STAGE}" in
    linux)
      [[ "$(uname -s)" == "Linux" ]] || die "the linux stage must run on a Debian host"
      ;;
    macos)
      [[ "$(uname -s)" == "Darwin" && "$(uname -m)" == "arm64" ]] || die "the macos stage must run on an Apple Silicon Mac"
      ;;
  esac

  "${PLATFORM_DIR}/scripts/install-release-dependencies.sh" "${RELEASE_STAGE}"
  require_command jq
  require_command tar
  require_command gh
  gh auth status --hostname github.com >/dev/null
  gh api "repos/${RELEASE_REPO}" --jq .full_name >/dev/null
  gh api "repos/${RELEASE_REPO}/commits/${COMMIT_SHA}" --jq .sha >/dev/null || die "commit ${COMMIT_SHA} is not pushed to ${RELEASE_REPO}"

  WORK_DIR="$(mktemp -d "${REPO_ROOT}/.release-work.XXXXXX")"
  local linux_source="${WORK_DIR}/linux-source"
  local macos_source="${WORK_DIR}/macos-source"
  local linux_updater_config="${WORK_DIR}/linux-updater.conf.json"
  local macos_updater_config="${WORK_DIR}/macos-updater.conf.json"
  local android_config="${WORK_DIR}/android.conf.json"
  local notes_file="${WORK_DIR}/release-notes.md"
  local assets_json="${WORK_DIR}/assets.json"
  local platforms_json="${WORK_DIR}/platforms.json"
  local latest_json="${WORK_DIR}/latest.json"

  rm -rf "${DIST_DIR}"
  mkdir -p "${DIST_DIR}"
  cat > "${notes_file}" <<EOF
Automated local build of \`${RELEASE_REPO}@${COMMIT_SHA}\` (app version ${APP_VERSION}).

Assets include macOS Apple Silicon, Linux x86_64 (deb/rpm/AppImage), Android ARM64 (debug-signed APK), and signed desktop auto-updater artifacts (\`latest.json\`).
EOF

  case "${RELEASE_STAGE}" in
    linux)
      require_command docker
      docker info >/dev/null 2>&1 || die "Docker Engine is not running"
      load_signing_key
      [[ "${COMMIT_COUNT}" -le 2100000000 ]] || die "commit count is too large for Android versionCode"
      write_linux_configs "${linux_updater_config}" "${android_config}"
      archive_source "${linux_source}"
      ensure_tag_and_release "${notes_file}"
      build_linux "${linux_source}" "${linux_updater_config}" "${android_config}"
      require_artifact '*.deb' 'Linux deb package'
      require_artifact '*.rpm' 'Linux rpm package'
      require_artifact '*.AppImage' 'Linux AppImage'
      require_artifact '*.AppImage.sig' 'Linux updater signature'
      require_artifact '*android-arm64-debug.apk' 'Android ARM64 APK'
      upload_artifacts
      build_updater_manifest "${assets_json}" "${platforms_json}" "${latest_json}" linux-x86_64
      upload_updater_manifest "${latest_json}"
      publish_release
      ;;
    macos)
      load_signing_key
      write_macos_updater_config "${macos_updater_config}"
      archive_source "${macos_source}"
      ensure_tag_and_release "${notes_file}"
      build_macos "${macos_source}" "${macos_updater_config}"
      require_artifact '*.dmg' 'macOS ARM64 DMG'
      require_artifact '*.app.tar.gz' 'macOS updater archive'
      require_artifact '*.app.tar.gz.sig' 'macOS updater signature'
      upload_artifacts
      build_updater_manifest "${assets_json}" "${platforms_json}" "${latest_json}" darwin-aarch64
      upload_updater_manifest "${latest_json}"
      publish_release
      ;;
  esac
}

main "$@"
