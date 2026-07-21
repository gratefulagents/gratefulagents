#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
installer="$script_dir/install-k3s.sh"
fetch_source="$script_dir/fetch-k3s-source.sh"
tmp_dir="$(mktemp -d)"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local expected="$1"
  grep -Fq -- "$expected" "$installer" || fail "installer is missing: $expected"
}

# Build a local origin where a branch and an annotated tag have the same release
# name but contain different charts. The installer's default must select the tag.
git init --quiet --bare "$tmp_dir/origin.git"
git init --quiet "$tmp_dir/work"
git -C "$tmp_dir/work" config user.name installer-test
git -C "$tmp_dir/work" config user.email installer-test@example.com
mkdir -p "$tmp_dir/work/dist/chart"
printf 'release-tag\n' > "$tmp_dir/work/dist/chart/source"
git -C "$tmp_dir/work" add dist/chart/source
git -C "$tmp_dir/work" commit --quiet -m 'tag chart'
git -C "$tmp_dir/work" tag -a v1.2.3 -m v1.2.3

printf 'same-named-branch\n' > "$tmp_dir/work/dist/chart/source"
git -C "$tmp_dir/work" commit --quiet -am 'branch chart'
git -C "$tmp_dir/work" branch v1.2.3
git -C "$tmp_dir/work" branch custom-chart
origin_url="file://$tmp_dir/origin.git"
git -C "$tmp_dir/work" remote add origin "$origin_url"
git -C "$tmp_dir/work" push --quiet origin \
  refs/tags/v1.2.3:refs/tags/v1.2.3 \
  refs/heads/v1.2.3:refs/heads/v1.2.3 \
  refs/heads/custom-chart:refs/heads/custom-chart

"$fetch_source" v1.2.3 '' "$origin_url" "$tmp_dir/exact"
[[ "$(cat "$tmp_dir/exact/dist/chart/source")" == 'release-tag' ]] || \
  fail 'default source fetch did not select the exact release tag'

"$fetch_source" v1.2.3 custom-chart "$origin_url" "$tmp_dir/override" >/dev/null
[[ "$(cat "$tmp_dir/override/dist/chart/source")" == 'same-named-branch' ]] || \
  fail 'explicit GRATEFULAGENTS_REF override was not selected'

if "$fetch_source" invalid/tag '' "$origin_url" "$tmp_dir/invalid" >/dev/null 2>&1; then
  fail 'invalid image tag unexpectedly succeeded'
fi

# Keep the integration contract visible: release resolution happens first, and
# the fetched source is used only when no explicit local source/chart supplies it.
assert_contains 'source_ref="${GRATEFULAGENTS_REF:-$IMAGE_TAG}"'
assert_contains '"$IMAGE_TAG" "$GRATEFULAGENTS_REF" "$GRATEFULAGENTS_REPOSITORY_URL" "$TMP_DIR/gratefulagents"'
assert_contains 'if [[ -z "$SOURCE_DIR" && -n "$CHART_DIR" ]]; then'
if grep -Fq -- '"$script_dir/../dist/chart/Chart.yaml"' "$installer"; then
  fail 'installer still auto-selects the chart from its current checkout'
fi

image_resolution_line="$(grep -nF 'if [[ -z "$IMAGE_TAG" ]]' "$installer" | head -n1 | cut -d: -f1)"
source_resolution_line="$(grep -nF 'source_ref="${GRATEFULAGENTS_REF:-$IMAGE_TAG}"' "$installer" | head -n1 | cut -d: -f1)"
[[ -n "$image_resolution_line" && -n "$source_resolution_line" ]] || fail 'could not locate release source resolution'
(( image_resolution_line < source_resolution_line )) || fail 'chart source is selected before the image release is resolved'

printf 'install-k3s release chart tests passed\n'
