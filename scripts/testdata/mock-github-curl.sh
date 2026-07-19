#!/usr/bin/env bash
set -Eeuo pipefail

case "${MOCK_GITHUB_RESPONSE:-success}" in
  success)
    printf '%s\n' '{"tag_name":"v1.2.3","draft":false,"prerelease":false}'
    ;;
  missing-tag)
    printf '%s\n' '{"draft":false,"prerelease":false}'
    ;;
  invalid-tag)
    printf '%s\n' '{"tag_name":"not a valid tag"}'
    ;;
  failure)
    printf 'mock GitHub failure\n' >&2
    exit 22
    ;;
  *)
    printf 'unknown mock response: %s\n' "$MOCK_GITHUB_RESPONSE" >&2
    exit 2
    ;;
esac
