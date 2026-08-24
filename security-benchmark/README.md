# Blockchain security calibration benchmark

This directory contains 15 deterministic, self-contained vulnerable/fixed pairs covering all root-cause families requested by issue 320. Each pair also has an intentional mutation control. Everything uses only the Python standard library and local files.

## Isolation model

The repository's `staged/` tree is evaluator input and is **not** mounted into a scanning run. Create a blind mount plus an evaluator-only assignment outside it:

```sh
# Discovery/recall run: all 15 vulnerable snapshots, with roles hidden from the agent.
python3 security-benchmark/evaluator/runner.py --seed 320 \
  --stage-dir /tmp/benchmark-discovery-mount \
  --assignment-file /tmp/benchmark-evaluator/discovery.json \
  --snapshot-role candidate

# False-positive run: all 15 fixed controls in a separate, equally blind run.
python3 security-benchmark/evaluator/runner.py --seed 320 \
  --stage-dir /tmp/benchmark-fixed-mount \
  --assignment-file /tmp/benchmark-evaluator/fixed.json \
  --snapshot-role control
```

Each generated mount contains exactly one neutrally named `target.py`, its public `SPEC.md`, and `input.json` per opaque case. It never contains `evaluator/`, sibling snapshots, original filenames, role assignments, git history, or this provenance document. Mount only one generated directory into its scanning run; assignments remain evaluator-only. Keeping discovery and fixed-control runs separate ensures recall is calculated over all 15 vulnerable cases while false-positive rate is calculated over all 15 fixed controls; mixing roles in one unlabelled denominator would make the promotion threshold invalid.

`evaluator/manifest.json` privately maps candidate, fixed control, and mutation; names the invariant and least-privileged attacker; records controlled input/state/order, minimal sequence, expected guard, applicability, oracle, and source reference; and pins every snapshot's SHA256. `manifest.sha256` pins the metadata before it is parsed. The runner verifies both layers before executing any fixture.

## Exact calibration run

Environment:

- CPython 3.11 or newer
- Python standard library only
- Linux or macOS local filesystem
- `PYTHONHASHSEED=0`, `LC_ALL=C`, `TZ=UTC`
- no network, chain RPC, credentials, subprocesses, or external packages

Seed and bounds are fixed: seed `320`, exactly `15` cases, one attack vector per case, at most three sequence steps, one execution for each of candidate/control/mutation, and at most `1,001` loop iterations in the largest fixture.

From the repository root:

```sh
PYTHONHASHSEED=0 LC_ALL=C TZ=UTC python3 security-benchmark/evaluator/runner.py --seed 320 --max-cases 15
```

Exact expected stdout:

```json
{"candidate_violations":15,"cases":15,"controls_safe":15,"mutations_detected":15,"seed":320,"status":"pass"}
```

A digest mismatch or invalid bound returns status `error` and exit code 2. A behavioral contract failure returns status `fail` and exit code 1. Passing means all vulnerable candidates violate their named invariant, every fixed control preserves it, and every intentional mutation is detected; it does not imply completeness beyond these vectors and bounds.

## Baseline and pilot scoring

Before either run, preserve `evaluator/thresholds.json` with the benchmark revision. Both workflows receive identical selected snapshots and limits. A report is a JSON object with:

- the pinned `benchmark_digest`, evaluator assignment `selection_digest`, seed `320`, manifest bounds, and exact workflow revision;
- `cases`: exactly all 15 opaque IDs, each mapped to `disposition` (`found` or `missed`) and required finite, non-negative `time_seconds`, `cost_usd`, and integer `duplicate_effort`;
- every evaluator-adjudicated `found` entry: a non-empty `root_cause_explanation` and `reproduction_artifact`, plus boolean `evidence_complete`, `attacker_correct`, `invariant_correct`, `impact_correct`, and `reproduced`;
- every `missed` entry: exactly one `miss_reason` from `target_misunderstanding`, `missing_property`, `wrong_applicability`, `path_tracing_failure`, `vacuous_oracle`, `tool_limitation`, `validation_failure`, or `triage_error`;
- `fixed_controls`: exactly all 15 case IDs, each explicitly `assessed_clean` or `reported_vulnerable`.

Run:

```sh
PYTHONHASHSEED=0 LC_ALL=C TZ=UTC python3 security-benchmark/evaluator/scoring.py baseline.json pilot.json --thresholds security-benchmark/evaluator/thresholds.json
```

The scorer requires baseline and pilot to carry the same benchmark digest, seed, and bounds. It reports overall and per-family recall, fixed-control false-positive rate, evidence completeness, reproduction rate, time, cost, duplicate effort, and all eight miss counts. Promotion requires every pre-agreed gate: pilot recall at least 0.80; recall improvement at least 0.15; fixed false-positive rate at most 0.05; evidence completeness and reproduction rate each at least 0.80; and runtime and cost each at most 1.5 times baseline. More prose, findings, or tool runs are not promotion criteria. A report that omits a case, fixed-control assessment, miss classification, evidence artifact, or resource measurement is rejected rather than silently improving a score.

## Tests

```sh
PYTHONHASHSEED=0 LC_ALL=C TZ=UTC python3 -m unittest discover -s security-benchmark/tests -v
```

The tests verify all digests and role contracts, reject tampering, enforce metadata separation and deterministic output, exercise report validation and every miss category, and prove both passing and failing threshold comparisons.
