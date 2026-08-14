#!/bin/sh
set -eu

image=${1:?usage: test-security-tools-runtime.sh IMAGE}
case_dir=$(mktemp -d)
cleanup() { rm -rf "$case_dir"; }
trap cleanup EXIT INT TERM

run_native() {
  docker run --rm --network none --entrypoint "$1" "$image" "$2" >/dev/null
}
run_root() {
  root=$1
  shift
  docker run --rm --network none --user 0 \
    --mount "type=bind,src=/dev/null,dst=/usr/local/share/ga-security/toolroots/$root/dev/null" \
    --entrypoint /usr/sbin/chroot "$image" \
    "/usr/local/share/ga-security/toolroots/$root" "$@"
}

# Execute every packaged EVM runtime, not only the registry/parser fixtures.
run_native aderyn --version
run_native forge --version
run_native echidna --version
run_root slither /usr/bin/env -i HOME=/home/ethsec \
  PATH=/home/ethsec/.local/bin:/home/ethsec/.foundry/bin:/usr/local/bin:/usr/bin:/bin \
  /home/ethsec/.local/bin/slither --version >/dev/null
run_root halmos /usr/bin/env -i HOME=/tmp \
  PATH=/opt/halmos/bin:/usr/local/bin:/usr/bin:/bin \
  /opt/halmos/bin/halmos --version >/dev/null

# cargo-fuzz runs the project's own fuzz targets, so packaging is proved by the
# pinned toolchain answering inside its own root. Two environment facts differ
# from the production sandbox and are supplied explicitly here: rustup's `cargo`
# shim resolves itself through /proc/self/exe, which this chroot does not mount
# (bubblewrap does), so the probe execs the toolchain's real rustc; and that
# rustc loads librustc_driver from its own toolchain lib directory, which is
# named after the dated toolchain and is therefore resolved by glob.
run_root cargo-fuzz /bin/sh -c \
  'set -eu; test -x /usr/local/cargo/bin/cargo-fuzz; tc=$(echo /usr/local/rustup/toolchains/nightly-2026-06-01-*); LD_LIBRARY_PATH="$tc/lib" exec "$tc/bin/rustc" --version' >/dev/null

run_ga_project() {
  tool=$1
  target_type=$2
  media_type=$3
  project=$4
  output="$case_dir/output-$tool"
  config="$case_dir/config-$tool.json"
  mkdir -p "$output"
  chmod 0777 "$output"
  digest=$(docker run --rm --network none --mount "type=bind,src=$project,dst=/case,readonly" \
    --entrypoint /usr/local/bin/ga-security "$image" --digest-target /case)
  cat >"$config" <<EOF
{"tool":"$tool","target":{"type":"$target_type","locator":"/case","revision":"runtime-smoke","digest":"$digest","media_type":"$media_type"}}
EOF
  status=0
  # Docker's outer namespace blocks nested unprivileged mount namespace setup.
  # Run this disposable integration container as root with only the capabilities
  # Bubblewrap needs; it still creates the production mount and PID namespaces,
  # while EVM dependency resolution uses the container's network.
  docker run --rm --user 0 --cap-add SYS_ADMIN --cap-add NET_ADMIN \
    --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
    --mount "type=bind,src=$project,dst=/case,readonly" \
    --mount "type=bind,src=$config,dst=/config.json,readonly" \
    --mount "type=bind,src=$output,dst=/output" \
    --entrypoint /usr/local/bin/ga-security "$image" --config /config.json --output /output >/dev/null || status=$?
  case "$status" in
    0|10|20) ;;
    *)
      echo "$tool production sandbox run failed with status $status" >&2
      docker run --rm --user 0 --mount "type=bind,src=$output,dst=/output,readonly" \
        --entrypoint /bin/cat "$image" /output/result.json >&2 || true
      exit "$status"
      ;;
  esac
  docker run --rm --user 0 --mount "type=bind,src=$output,dst=/output,readonly" \
    --entrypoint /bin/grep "$image" -q '"tool": "'$tool'"' /output/result.json
}

# The upstream Slither toolbox currently embeds an amd64 solc artifact even in
# its arm64 manifest. Keep the architecture-neutral root/version probe above,
# but exercise project compilation only where the embedded compiler is native.
if [ "$(uname -m)" = "x86_64" ]; then
  mkdir -p "$case_dir/slither"
  cat >"$case_dir/slither/Vault.sol" <<'EOF'
pragma solidity >=0.8.0;
contract Vault {
    mapping(address => uint256) public balance;
    function withdraw() external {
        (bool ok,) = msg.sender.call{value: balance[msg.sender]}("");
        require(ok);
        balance[msg.sender] = 0;
    }
}
EOF
  run_ga_project slither solidity_project application/vnd.gratefulagents.solidity-project.v1+directory "$case_dir/slither"
fi

# The root/version probe above verifies the architecture-specific Halmos closure.
# Project-level Halmos normalization is covered by adapter and runner fixtures;
# Halmos 0.3.3 writes a non-object result for this trivial identity harness.

echo "security-tools runtime smoke tests passed"
