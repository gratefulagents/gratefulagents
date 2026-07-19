#!/bin/bash

# Generate Connect-Go code from the proto files under rpc/.
#
# The rpc/*.proto files at the repo root are the single source of truth for
# the API contract. The TypeScript stubs for the frontend workspace
# (platform-app/) are generated separately via buf (`make gen-rpc` runs both).
#
# Prerequisites:
# - protoc (Protocol Buffer Compiler)
#   macOS: brew install protobuf
#   Linux: apt-get install protobuf-compiler

set -e

echo "Generating Connect-Go code from proto files..."

# Ensure the protoc plugins are installed
if ! command -v protoc-gen-go &> /dev/null || ! command -v protoc-gen-connect-go &> /dev/null; then
    echo "Installing protoc plugins..."
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
else
    echo "protoc plugins are already installed."
fi

if protoc --version &>/dev/null; then
    echo "protoc is already installed."
else
    echo "protoc is not installed. Installing..."
    OS="$(uname -s)"

    if [ "$OS" = "Darwin" ]; then
        echo "Detected macOS. Installing via Homebrew..."
        brew install protobuf
    fi
fi

# Add GOPATH/bin to PATH if not already there
export PATH="$PATH:$(go env GOPATH)/bin"

PROTOC_INCLUDE_ARGS=(-I. -I/usr/local/include)

for proto_file in rpc/*/service.proto; do
  echo "Processing $proto_file..."
  protoc "${PROTOC_INCLUDE_ARGS[@]}" --go_out=. --go_opt=paths=source_relative \
         --connect-go_out=. --connect-go_opt=paths=source_relative "$proto_file"
done

echo "Code generation complete!"
