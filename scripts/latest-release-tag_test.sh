#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT
ln -s "$script_dir/testdata/mock-github-curl.sh" "$tmp_dir/curl"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

tag="$(PATH="$tmp_dir:$PATH" MOCK_GITHUB_RESPONSE=success \
  "$script_dir/latest-release-tag.sh")"
[[ "$tag" == "v1.2.3" ]] || fail "expected v1.2.3, got $tag"

if PATH="$tmp_dir:$PATH" MOCK_GITHUB_RESPONSE=missing-tag \
  "$script_dir/latest-release-tag.sh" >/dev/null 2>&1; then
  fail "missing tag response unexpectedly succeeded"
fi

if PATH="$tmp_dir:$PATH" MOCK_GITHUB_RESPONSE=invalid-tag \
  "$script_dir/latest-release-tag.sh" >/dev/null 2>&1; then
  fail "invalid image tag unexpectedly succeeded"
fi

if PATH="$tmp_dir:$PATH" MOCK_GITHUB_RESPONSE=failure \
  "$script_dir/latest-release-tag.sh" >/dev/null 2>&1; then
  fail "GitHub request failure unexpectedly succeeded"
fi

if PATH="$tmp_dir:$PATH" GRATEFULAGENTS_REPOSITORY=invalid \
  "$script_dir/latest-release-tag.sh" >/dev/null 2>&1; then
  fail "invalid repository unexpectedly succeeded"
fi

printf 'latest-release-tag tests passed\n'
