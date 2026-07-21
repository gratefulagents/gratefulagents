#!/usr/bin/env bash
# Fetch the source used by the k3s installer. By default, check out the exact
# application release tag; an explicit source ref retains branch-or-tag behavior
# for development and custom deployments.
set -Eeuo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 IMAGE_TAG SOURCE_REF REPOSITORY_URL DESTINATION" >&2
  exit 2
fi

image_tag="$1"
source_ref="$2"
repository_url="$3"
destination="$4"

[[ "$image_tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || {
  echo "fetch-k3s-source: IMAGE_TAG is not a valid release tag: $image_tag" >&2
  exit 1
}
[[ -n "$repository_url" ]] || {
  echo "fetch-k3s-source: repository URL must not be empty" >&2
  exit 1
}
[[ ! -e "$destination" ]] || {
  echo "fetch-k3s-source: destination already exists: $destination" >&2
  exit 1
}

if [[ -n "$source_ref" ]]; then
  git clone --depth 1 --branch "$source_ref" "$repository_url" "$destination"
  exit 0
fi

# Fetch the fully-qualified tag ref so a same-named branch can never win.
git init --quiet "$destination"
git -C "$destination" remote add origin "$repository_url"
if ! git -C "$destination" fetch --quiet --depth 1 origin "refs/tags/$image_tag"; then
  rm -rf "$destination"
  echo "fetch-k3s-source: could not fetch release tag $image_tag" >&2
  exit 1
fi
git -C "$destination" checkout --quiet --detach FETCH_HEAD
