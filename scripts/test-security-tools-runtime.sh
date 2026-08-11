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
  docker run --rm --network none --user 0 --entrypoint /usr/sbin/chroot "$image" \
    "/usr/local/share/ga-security/toolroots/$root" "$@"
}

# Execute every packaged EVM runtime, not only the registry/parser fixtures.
run_native aderyn --version
run_native forge --version
run_native echidna --version
run_root mythril /usr/local/bin/myth version >/dev/null
run_root slither /usr/bin/env -i HOME=/home/ethsec \
  PATH=/home/ethsec/.local/bin:/home/ethsec/.foundry/bin:/usr/local/bin:/usr/bin:/bin \
  /home/ethsec/.local/bin/slither --version >/dev/null
run_root semgrep /usr/local/bin/semgrep --version >/dev/null
run_root halmos /usr/bin/env -i HOME=/tmp \
  PATH=/opt/halmos/bin:/usr/local/bin:/usr/bin:/bin \
  /opt/halmos/bin/halmos --version >/dev/null

# Prove that the pinned Semgrep root can execute a local, offline rule and scan.
mkdir -p "$case_dir/semgrep"
cat >"$case_dir/semgrep/.semgrep.yml" <<'EOF'
rules:
  - id: gratefulagents.runtime-smoke
    languages: [python]
    severity: ERROR
    message: runtime smoke finding
    pattern: dangerous($X)
EOF
cat >"$case_dir/semgrep/target.py" <<'EOF'
dangerous(user_input)
EOF
semgrep_json=$(docker run --rm --network none --user 0 \
  --mount "type=bind,src=$case_dir/semgrep,dst=/usr/local/share/ga-security/toolroots/semgrep/tmp/case,readonly" \
  --entrypoint /usr/sbin/chroot "$image" /usr/local/share/ga-security/toolroots/semgrep \
  /usr/local/bin/semgrep scan --config /tmp/case/.semgrep.yml --json --metrics off /tmp/case)
printf '%s' "$semgrep_json" | grep -q 'gratefulagents.runtime-smoke'

# Prove that the Mythril root performs a bounded bytecode analysis offline.
printf '00\n' >"$case_dir/stop.hex"
mythril_json=$(docker run --rm --network none --user 0 \
  --mount "type=bind,src=$case_dir/stop.hex,dst=/usr/local/share/ga-security/toolroots/mythril/tmp/stop.hex,readonly" \
  --entrypoint /usr/sbin/chroot "$image" /usr/local/share/ga-security/toolroots/mythril \
  /usr/local/bin/myth analyze --codefile /tmp/stop.hex -o json --strategy bfs --max-depth 4 \
  --call-depth-limit 1 --loop-bound 1 --transaction-count 1 --execution-timeout 30 \
  --solver-timeout 2000 --create-timeout 10 --no-onchain-data)
printf '%s' "$mythril_json" | grep -q '"success"'

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
  docker run --rm --network none --security-opt seccomp=unconfined \
    --mount "type=bind,src=$project,dst=/case,readonly" \
    --mount "type=bind,src=$config,dst=/config.json,readonly" \
    --mount "type=bind,src=$output,dst=/output" \
    --entrypoint /usr/local/bin/ga-security "$image" --config /config.json --output /output >/dev/null || status=$?
  case "$status" in 0|10|20) ;; *) echo "$tool production sandbox run failed with status $status" >&2; exit "$status" ;; esac
  grep -q '"tool": "'$tool'"' "$output/result.json"
}

# Exercise directory scanners through ga-security's production Bubblewrap path.
run_ga_project semgrep semgrep_project application/vnd.gratefulagents.semgrep-project.v1+directory "$case_dir/semgrep"

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

mkdir -p "$case_dir/halmos/src" "$case_dir/halmos/test"
cat >"$case_dir/halmos/foundry.toml" <<'EOF'
[profile.default]
src = "src"
test = "test"
solc = "/usr/local/bin/solc"
offline = true
EOF
cat >"$case_dir/halmos/test/Identity.t.sol" <<'EOF'
pragma solidity >=0.8.0;
contract IdentityTest {
    function check_identity(uint256 value) public pure {
        assert(value == value);
    }
}
EOF
run_ga_project halmos foundry_project application/vnd.gratefulagents.foundry-security-project.v1+directory "$case_dir/halmos"

echo "security-tools runtime smoke tests passed"
