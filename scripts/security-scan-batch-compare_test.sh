#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
tmp_dir="$(mktemp -d)"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

# A fake kubectl that records every invocation and answers the read paths the
# script depends on: SecurityScan and AgentRun listings and the psql exec.
cat >"$tmp_dir/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_KUBECTL_LOG"
case "$*" in
  *"get securityscans"*)
    cat <<'JSON'
{"items":[
 {"metadata":{"name":"ethereum-geth"},"status":{"lastExecution":{"startedAt":"2026-09-02T11:54:00Z","phase":"Succeeded","evidenceOutcome":"partial","coverageGaps":["a","b"]},"findings":{"total":0}}},
 {"metadata":{"name":"old-scan"},"status":{"lastExecution":{"startedAt":"2026-08-01T00:00:00Z","phase":"Succeeded"}}}
]}
JSON
    ;;
  *"get agentruns"*)
    cat <<'JSON'
{"items":[
 {"metadata":{"name":"r1","creationTimestamp":"2026-09-02T11:54:00Z","labels":{"security.gratefulagents.dev/scan":"ethereum-geth","security.gratefulagents.dev/scan-task":"consensus-and-execution-investigator"}},"spec":{},"status":{"metrics":{"costUsd":"2.5","toolCallCount":63},"modeSnapshot":{"constraints":{"maxTurns":250}}}}
]}
JSON
    ;;
  *"get pods"*)
    printf 'gratefulagents-postgres-0\n'
    ;;
  *"exec"*)
    cat >"$FAKE_KUBECTL_SQL"
    printf ' status | n \n--------+---\n(0 rows)\n'
    ;;
  *"annotate"*)
    ;;
  *)
    echo "unexpected kubectl args: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$tmp_dir/kubectl"

export FAKE_KUBECTL_LOG="$tmp_dir/kubectl.log"
export FAKE_KUBECTL_SQL="$tmp_dir/last.sql"
: >"$FAKE_KUBECTL_LOG"

# measure: filters executions by the since timestamp, aggregates task budgets,
# and sends scan-filtered SQL to the Postgres pod.
out="$(PATH="$tmp_dir:$PATH" "$script_dir/security-scan-batch-compare.sh" measure ns 2026-09-02T11:50:00Z ethereum-geth)"
grep -q 'ethereum-geth *Succeeded *partial *2' <<<"$out" || fail "execution row missing: $out"
grep -q 'old-scan' <<<"$out" && fail "execution before the since timestamp was not filtered"
grep -q 'consensus-and-execution-investigator *1 *2.50 *63.0 *250.0' <<<"$out" || fail "task budget row missing: $out"
grep -q "scan_name IN ('ethereum-geth')" "$FAKE_KUBECTL_SQL" || fail "findings SQL is not scan-filtered: $(cat "$FAKE_KUBECTL_SQL")"
grep -q "first_seen_at >= '2026-09-02T11:50:00Z'" "$FAKE_KUBECTL_SQL" || fail "findings SQL is not time-filtered"
grep -q -- "-n gratefulagents-system gratefulagents-postgres-0" "$FAKE_KUBECTL_LOG" || fail "psql exec did not target the discovered pod"

# measure without scan names matches every scan.
: >"$FAKE_KUBECTL_LOG"
PATH="$tmp_dir:$PATH" "$script_dir/security-scan-batch-compare.sh" measure ns 2026-09-02T11:50:00Z >/dev/null
grep -q "AND TRUE" "$FAKE_KUBECTL_SQL" || fail "unfiltered findings SQL should use TRUE: $(cat "$FAKE_KUBECTL_SQL")"

# trigger: annotates every named scan with a run-now token and echoes the
# matching measure command.
: >"$FAKE_KUBECTL_LOG"
out="$(PATH="$tmp_dir:$PATH" "$script_dir/security-scan-batch-compare.sh" trigger ns ethereum-geth solana-agave)"
[[ "$(grep -c 'annotate securityscan -n ns' "$FAKE_KUBECTL_LOG")" == 2 ]] || fail "expected two annotate calls: $(cat "$FAKE_KUBECTL_LOG")"
grep -q 'security.gratefulagents.dev/run-now=' "$FAKE_KUBECTL_LOG" || fail "run-now annotation missing"
grep -q 'measure ns ' <<<"$out" || fail "trigger did not echo the measure command: $out"

# trigger without scans and unknown commands fail; baseline and help print.
PATH="$tmp_dir:$PATH" "$script_dir/security-scan-batch-compare.sh" trigger ns >/dev/null 2>&1 && fail "trigger without scans should fail"
PATH="$tmp_dir:$PATH" "$script_dir/security-scan-batch-compare.sh" bogus >/dev/null 2>&1 && fail "unknown command should fail"
"$script_dir/security-scan-batch-compare.sh" baseline | grep -q '2026-09-02' || fail "baseline text missing"
"$script_dir/security-scan-batch-compare.sh" --help | grep -q 'Usage' || fail "help text missing"

echo "PASS: security-scan-batch-compare.sh"
