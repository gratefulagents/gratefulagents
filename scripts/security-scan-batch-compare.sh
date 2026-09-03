#!/usr/bin/env bash
# Trigger and/or measure a batch of SecurityScan executions so a workflow
# change can be compared against a recorded baseline.
#
# The 2026-09-02 blockchain-protocol-audit batch is the reference baseline
# (17 scans, 0 findings, investigators stopping at 10-25 of 90 minutes on the
# 6/3/2 schema minimum, ~2/3 of experiments re-running upstream tests). This
# script reproduces that measurement for any later batch so the redesign in
# PR #370 can be judged on the same numbers:
#
#   * per scan: execution phase, evidence outcome, coverage gaps, findings
#   * per task: AgentRun cost, tool calls versus maxTurns, wall clock
#   * hypotheses: status/result distribution, share with detail.anchor and a prior
#     (research tables are keyed by target revision, not scan name, so the SQL
#     sections cover every execution in the window)
#   * coverage: experiment_kind distribution, share carrying a command
#   * artifacts: harness rounds, blockers, next_experiment manifests
#
# Usage:
#   scripts/security-scan-batch-compare.sh trigger <namespace> <scan>...
#       Annotates each SecurityScan with security.gratefulagents.dev/run-now
#       so the controller starts a manual execution.
#   scripts/security-scan-batch-compare.sh measure <namespace> <since-rfc3339> [scan...]
#       Prints the metrics above for executions created at or after <since>.
#   scripts/security-scan-batch-compare.sh baseline
#       Prints the recorded 2026-09-02 baseline for side-by-side reading.
#
# Environment:
#   KUBECTL           kubectl binary (default: kubectl)
#   PG_NAMESPACE      namespace of the platform Postgres pod (default: gratefulagents-system)
#   PG_POD_SELECTOR   label selector for the Postgres pod (default: app.kubernetes.io/component=postgres)
#   PG_POD            explicit Postgres pod name (overrides the selector)
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
PG_NAMESPACE="${PG_NAMESPACE:-gratefulagents-system}"
PG_POD_SELECTOR="${PG_POD_SELECTOR:-app.kubernetes.io/component=postgres}"

usage() {
  sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'
}

psql_pod() {
  if [ -n "${PG_POD:-}" ]; then
    printf '%s\n' "$PG_POD"
    return
  fi
  local pod
  pod="$("$KUBECTL" get pods -n "$PG_NAMESPACE" -l "$PG_POD_SELECTOR" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [ -z "$pod" ]; then
    pod="$("$KUBECTL" get pods -n "$PG_NAMESPACE" -o name 2>/dev/null | sed -n 's|^pod/||p' | grep -m1 postgres || true)"
  fi
  if [ -z "$pod" ]; then
    echo "no Postgres pod found in namespace $PG_NAMESPACE" >&2
    exit 1
  fi
  printf '%s\n' "$pod"
}

# run_sql executes one SQL statement inside the Postgres pod. The statement is
# passed on stdin so quoting stays intact.
run_sql() {
  local pod
  pod="$(psql_pod)"
  "$KUBECTL" exec -i -n "$PG_NAMESPACE" "$pod" -- sh -c 'psql -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-postgres}" -X -P pager=off -v ON_ERROR_STOP=1 -f -'
}

sql_list() {
  # Turns "a b c" into "'a','b','c'" for IN (...) clauses; empty input yields
  # a predicate that matches everything.
  if [ "$#" -eq 0 ]; then
    printf 'TRUE'
    return
  fi
  local out="" name
  for name in "$@"; do
    name="${name//\'/\'\'}"
    out="${out:+$out,}'$name'"
  done
  printf 'scan_name IN (%s)' "$out"
}

cmd_trigger() {
  local namespace="$1"; shift
  if [ "$#" -eq 0 ]; then
    echo "trigger requires at least one SecurityScan name" >&2
    exit 1
  fi
  local stamp
  stamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local scan
  for scan in "$@"; do
    "$KUBECTL" annotate securityscan -n "$namespace" "$scan" --overwrite "security.gratefulagents.dev/run-now=$stamp"
  done
  echo "triggered ${#} scan(s) at $stamp; measure with: $0 measure $namespace $stamp $*"
}

cmd_measure() {
  local namespace="$1" since="$2"; shift 2
  local scan_filter
  scan_filter="$(sql_list "$@")"

  echo "== Executions (scan status) since $since in $namespace"
  "$KUBECTL" get securityscans -n "$namespace" -o json | python3 -c '
import json, sys
since, wanted = sys.argv[1], set(sys.argv[2:])
d = json.load(sys.stdin)
rows = []
for s in d["items"]:
    name = s["metadata"]["name"]
    if wanted and name not in wanted:
        continue
    le = (s.get("status") or {}).get("lastExecution") or {}
    if (le.get("startedAt") or "") < since:
        continue
    f = (s.get("status") or {}).get("findings") or {}
    rows.append((name, le.get("phase"), le.get("evidenceOutcome"), len(le.get("coverageGaps") or []), f.get("total", 0), f.get("open", 0)))
print("%-40s %-10s %-9s %5s %8s %5s" % ("scan", "phase", "evidence", "gaps", "findings", "open"))
for r in sorted(rows):
    print("%-40s %-10s %-9s %5d %8d %5d" % (r[0], r[1] or "-", r[2] or "-", r[3], r[4], r[5]))
' "$since" "$@"

  echo
  echo "== Task budget usage (AgentRuns) since $since"
  "$KUBECTL" get agentruns -n "$namespace" -o json | python3 -c '
import json, sys, collections
since, wanted = sys.argv[1], set(sys.argv[2:])
d = json.load(sys.stdin)
by = collections.defaultdict(lambda: [0, 0.0, 0, 0])
for r in d["items"]:
    labels = r["metadata"].get("labels") or {}
    scan = labels.get("security.gratefulagents.dev/scan")
    if not scan or (wanted and scan not in wanted) or r["metadata"].get("creationTimestamp", "") < since:
        continue
    task = labels.get("security.gratefulagents.dev/scan-task", "?")
    st = r.get("status") or {}
    m = st.get("metrics") or {}
    limits = (r.get("spec") or {}).get("limits") or {}
    snap = ((st.get("modeSnapshot") or {}).get("constraints") or {})
    max_turns = limits.get("maxTurns") or snap.get("maxTurns") or 0
    row = by[task]
    row[0] += 1
    row[1] += float(m.get("costUsd") or 0)
    row[2] += int(m.get("toolCallCount") or 0)
    row[3] += int(max_turns)
print("%-45s %4s %9s %10s %10s" % ("task", "runs", "cost_usd", "avg_calls", "avg_budget"))
for task, (n, cost, calls, budget) in sorted(by.items()):
    print("%-45s %4d %9.2f %10.1f %10.1f" % (task, n, cost, calls / n, budget / n if n else 0))
' "$since" "$@"

  echo
  echo "== Hypotheses since $since"
  run_sql <<SQL
SELECT h.status, h.result, count(*) AS n,
       count(*) FILTER (WHERE h.detail ? 'anchor') AS anchored,
       count(*) FILTER (WHERE h.detail ? 'prior_id' OR h.detail ? 'prior') AS with_prior,
       count(*) FILTER (WHERE h.detail ? 'guard_citation') AS guard_cited
FROM security_research_hypotheses h
WHERE h.created_at >= '$since'
GROUP BY 1, 2 ORDER BY 3 DESC;
SQL

  echo
  echo "== Coverage experiment kinds since $since"
  run_sql <<SQL
SELECT coalesce(c.bounds->'experiment'->>'kind', '(none)') AS experiment_kind, c.verdict, count(*) AS n,
       count(*) FILTER (WHERE coalesce(c.bounds->'experiment'->>'command', '') <> '') AS with_command
FROM security_research_coverage c
WHERE c.created_at >= '$since'
GROUP BY 1, 2 ORDER BY 3 DESC;
SQL

  echo
  echo "== Research artifacts by task and kind since $since"
  run_sql <<SQL
SELECT a.task_name, a.kind, count(*) AS n,
       count(*) FILTER (WHERE a.payload->>'harness_origin' IS NOT NULL) AS harness_rounds,
       count(*) FILTER (WHERE a.idempotency_key LIKE 'next-experiment-%') AS next_experiments
FROM security_research_artifacts a
WHERE a.created_at >= '$since'
GROUP BY 1, 2 ORDER BY 1, 2;
SQL

  echo
  echo "== Findings since $since"
  run_sql <<SQL
SELECT scan_name, severity, status, count(*) AS n,
       count(*) FILTER (WHERE duplicate_of IS NOT NULL) AS duplicates
FROM security_findings
WHERE first_seen_at >= '$since' AND $scan_filter
GROUP BY 1, 2, 3 ORDER BY 1, 2, 3;
SQL
}

cmd_baseline() {
  cat <<'EOF'
Baseline: blockchain-protocol-audit batch started 2026-09-02T11:53Z (17 scans)
  phase/evidence : 8 Failed/failed at runtime-preflight (env blockers), 9 Succeeded/partial
  findings       : 0 new findings (all scan-status totals were carried over from 08-29)
  investigators  : 4 x 90m/250 turns; actual 10-25 min, 60-110 tool calls, $2-8 each
  handoff quotas : hypotheses_examined 6-7, dynamic_experiments 3-6, methods 2-3 (schema minimum)
  hypotheses     : 147 falsified / 22 weakened / 20 blocked; 0 of ~42 sampled anchored to a prior
  experiments    : ~2/3 re-ran the project's own tests; 72% of falsifications had no command
  native fuzz    : 12m task, ~120s campaigns, 0-528 executions, adapter/staging blockers on every scan
  cost           : ~$300 for the batch
Compare: measure with this script after the next batch and look for anchored>0,
experiment_kind != existing_suite/(none), with_command close to n, avg_calls
approaching avg_budget on investigator tasks, harness_rounds >= 3 per scan, and
findings with duplicates tracked via duplicate_of instead of false_positive.
EOF
}

main() {
  local cmd="${1:-}"
  case "$cmd" in
    trigger) shift; [ "$#" -ge 1 ] || { usage; exit 1; }; cmd_trigger "$@" ;;
    measure) shift; [ "$#" -ge 2 ] || { usage; exit 1; }; cmd_measure "$@" ;;
    baseline) cmd_baseline ;;
    -h|--help|help|"") usage ;;
    *) echo "unknown command: $cmd" >&2; usage; exit 1 ;;
  esac
}

main "$@"
