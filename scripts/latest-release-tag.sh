#!/usr/bin/env bash
# Print the latest published GitHub release tag for the application images.
set -Eeuo pipefail

REPOSITORY="${GRATEFULAGENTS_REPOSITORY:-gratefulagents/gratefulagents}"
GITHUB_TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"

die() { printf 'latest-release-tag: %s\n' "$*" >&2; exit 1; }

[[ "$REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || \
  die "invalid repository: $REPOSITORY"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v sed >/dev/null 2>&1 || die "sed is required"

curl_args=(
  --fail
  --silent
  --show-error
  --location
  -H 'Accept: application/vnd.github+json'
  -H 'X-GitHub-Api-Version: 2022-11-28'
  -H 'User-Agent: gratefulagents-installer'
)
if [[ -n "$GITHUB_TOKEN" ]]; then
  curl_args+=( -H "Authorization: Bearer $GITHUB_TOKEN" )
fi

release_json="$(curl "${curl_args[@]}" \
  "https://api.github.com/repos/${REPOSITORY}/releases/latest")" || \
  die "could not read the latest $REPOSITORY release; set GITHUB_TOKEN if GitHub rate limits this host"
tag_name="$(printf '%s\n' "$release_json" | \
  sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p')"

[[ -n "$tag_name" ]] || die "the latest $REPOSITORY release has no tag_name"
[[ "$tag_name" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || \
  die "the latest release tag is not a valid container image tag: $tag_name"

printf '%s\n' "$tag_name"
