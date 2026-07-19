#!/bin/sh
set -eu

export PATH="$HOME/.cargo/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"

TAURI_ROOT="${SRCROOT}/../../.."
RUST_ROOT="${SRCROOT}/../.."
CONFIGURATION_NAME="${CONFIGURATION:-debug}"
CONFIGURATION_LOWER="$(printf '%s' "$CONFIGURATION_NAME" | tr '[:upper:]' '[:lower:]')"

PROFILE_DIR="debug"
RELEASE_ARGS=""
if [ "$CONFIGURATION_LOWER" = "release" ]; then
  PROFILE_DIR="release"
  RELEASE_ARGS="--release"
fi

if [ ! -f "$TAURI_ROOT/dist/index.html" ]; then
  (cd "$TAURI_ROOT" && pnpm build)
fi

rm -rf "$SRCROOT/assets"
mkdir -p "$SRCROOT/assets"
cp -R "$TAURI_ROOT/dist/." "$SRCROOT/assets/"

cd "$RUST_ROOT"

for ARCH in ${ARCHS:-arm64}; do
  case "$ARCH:$PLATFORM_NAME" in
    arm64:iphonesimulator)
      RUST_TARGET="aarch64-apple-ios-sim"
      OUTPUT_ARCH="arm64"
      ;;
    arm64:*)
      RUST_TARGET="aarch64-apple-ios"
      OUTPUT_ARCH="arm64"
      ;;
    x86_64:*)
      RUST_TARGET="x86_64-apple-ios"
      OUTPUT_ARCH="x86_64"
      ;;
    *)
      echo "Unsupported iOS Rust target for ARCH=$ARCH PLATFORM_NAME=$PLATFORM_NAME" >&2
      exit 1
      ;;
  esac

  cargo build --lib --target "$RUST_TARGET" --features custom-protocol $RELEASE_ARGS

  mkdir -p "$SRCROOT/Externals/$OUTPUT_ARCH/$CONFIGURATION_NAME"
  cp "target/$RUST_TARGET/$PROFILE_DIR/libgratefulagents_lib.a" \
    "$SRCROOT/Externals/$OUTPUT_ARCH/$CONFIGURATION_NAME/libapp.a"
done
