from __future__ import annotations

import argparse
import json
import math
from collections import Counter
from pathlib import Path
from typing import Any

from runner import LOCK_PATH, load_manifest

MISS_REASONS = (
    "target_misunderstanding",
    "missing_property",
    "wrong_applicability",
    "path_tracing_failure",
    "vacuous_oracle",
    "tool_limitation",
    "validation_failure",
    "triage_error",
)
EVIDENCE_FIELDS = ("evidence_complete", "attacker_correct", "invariant_correct", "impact_correct")


class ReportError(ValueError):
    pass


def _ratio(numerator: float, denominator: float) -> float:
    if denominator == 0:
        return 1.0 if numerator == 0 else math.inf
    return numerator / denominator


def _required_nonnegative(entry: dict[str, Any], key: str) -> float:
    if key not in entry or type(entry[key]) not in (int, float):
        raise ReportError(f"{key} must be a required number")
    value = float(entry[key])
    if not math.isfinite(value) or value < 0:
        raise ReportError(f"{key} must be finite and non-negative")
    return value


def score(report: dict[str, Any], manifest: dict[str, Any] | None = None) -> dict[str, Any]:
    manifest = manifest or load_manifest()
    benchmark_digest = report.get("benchmark_digest")
    expected_digest = LOCK_PATH.read_text(encoding="ascii").strip()
    if benchmark_digest != expected_digest:
        raise ReportError("benchmark_digest does not match the pinned benchmark")
    selection_digest = report.get("selection_digest")
    if not isinstance(selection_digest, str) or len(selection_digest) != 64:
        raise ReportError("selection_digest is required")
    expected_bounds = {
        "max_cases": manifest["max_cases"],
        "max_sequence_steps": manifest["max_sequence_steps"],
    }
    if report.get("seed") != manifest["seed"] or report.get("bounds") != expected_bounds:
        raise ReportError("report seed/bounds do not match the benchmark")
    if not isinstance(report.get("workflow_revision"), str) or not report["workflow_revision"]:
        raise ReportError("workflow_revision is required")
    expected_ids = {case["id"] for case in manifest["cases"]}
    entries = report.get("cases")
    if not isinstance(entries, dict) or set(entries) != expected_ids:
        missing = sorted(expected_ids - set(entries or {}))
        extra = sorted(set(entries or {}) - expected_ids)
        raise ReportError(f"report case coverage mismatch; missing={missing}, extra={extra}")
    fixed_controls = report.get("fixed_controls")
    if not isinstance(fixed_controls, dict) or set(fixed_controls) != expected_ids:
        raise ReportError("fixed_controls must explicitly assess every case")
    if not set(fixed_controls.values()) <= {"assessed_clean", "reported_vulnerable"}:
        raise ReportError("fixed control outcomes must be assessed_clean or reported_vulnerable")
    false_positives = [case_id for case_id, outcome in fixed_controls.items() if outcome == "reported_vulnerable"]

    found = 0
    evidence_points = 0
    reproduced = 0
    miss_reasons: Counter[str] = Counter()
    family_totals: Counter[str] = Counter()
    family_found: Counter[str] = Counter()
    case_by_id = {case["id"]: case for case in manifest["cases"]}
    total_time = 0.0
    total_cost = 0.0
    duplicate_effort = 0

    for case_id, entry in entries.items():
        family = case_by_id[case_id]["family"]
        family_totals[family] += 1
        disposition = entry.get("disposition")
        if disposition not in ("found", "missed"):
            raise ReportError(f"{case_id}: disposition must be found or missed")
        total_time += _required_nonnegative(entry, "time_seconds")
        total_cost += _required_nonnegative(entry, "cost_usd")
        duplicates = _required_nonnegative(entry, "duplicate_effort")
        if not duplicates.is_integer():
            raise ReportError(f"{case_id}: duplicate_effort must be an integer")
        duplicate_effort += int(duplicates)
        if disposition == "missed":
            reason = entry.get("miss_reason")
            if reason not in MISS_REASONS:
                raise ReportError(f"{case_id}: missed cases require one agreed miss_reason")
            miss_reasons[reason] += 1
            continue
        if "miss_reason" in entry:
            raise ReportError(f"{case_id}: found cases cannot have miss_reason")
        found += 1
        family_found[family] += 1
        for field in ("root_cause_explanation", "reproduction_artifact"):
            if not isinstance(entry.get(field), str) or not entry[field]:
                raise ReportError(f"{case_id}: {field} is required for evaluator-adjudicated findings")
        for field in EVIDENCE_FIELDS:
            value = entry.get(field)
            if not isinstance(value, bool):
                raise ReportError(f"{case_id}: {field} must be boolean")
            evidence_points += int(value)
        if not isinstance(entry.get("reproduced"), bool):
            raise ReportError(f"{case_id}: reproduced must be boolean")
        reproduced += int(entry["reproduced"])

    family_recall = {
        family: _ratio(family_found[family], total)
        for family, total in sorted(family_totals.items())
    }
    return {
        "benchmark_digest": benchmark_digest,
        "bounds": report["bounds"],
        "cost_usd": round(total_cost, 6),
        "duplicate_effort": duplicate_effort,
        "evidence_completeness": _ratio(evidence_points, found * len(EVIDENCE_FIELDS)),
        "false_positive_rate": _ratio(len(false_positives), len(expected_ids)),
        "family_recall": family_recall,
        "miss_taxonomy": {reason: miss_reasons[reason] for reason in MISS_REASONS},
        "recall": _ratio(found, len(expected_ids)),
        "reproduction_rate": _ratio(reproduced, found),
        "runtime_seconds": total_time,
        "seed": report["seed"],
        "selection_digest": selection_digest,
        "workflow_revision": report["workflow_revision"],
    }


def compare(baseline: dict[str, Any], pilot: dict[str, Any], thresholds: dict[str, float]) -> dict[str, Any]:
    for field in ("benchmark_digest", "selection_digest", "bounds", "seed"):
        if baseline[field] != pilot[field]:
            raise ReportError(f"baseline and pilot {field} differ")
    runtime_ratio = _ratio(pilot["runtime_seconds"], baseline["runtime_seconds"])
    cost_ratio = _ratio(pilot["cost_usd"], baseline["cost_usd"])
    checks = {
        "minimum_pilot_recall": pilot["recall"] >= thresholds["minimum_pilot_recall"],
        "minimum_recall_improvement": pilot["recall"] - baseline["recall"] >= thresholds["minimum_recall_improvement"],
        "maximum_fixed_false_positive_rate": pilot["false_positive_rate"] <= thresholds["maximum_fixed_false_positive_rate"],
        "minimum_evidence_completeness": pilot["evidence_completeness"] >= thresholds["minimum_evidence_completeness"],
        "minimum_reproduction_rate": pilot["reproduction_rate"] >= thresholds["minimum_reproduction_rate"],
        "maximum_runtime_ratio": runtime_ratio <= thresholds["maximum_runtime_ratio"],
        "maximum_cost_ratio": cost_ratio <= thresholds["maximum_cost_ratio"],
    }
    return {
        "baseline": baseline,
        "checks": checks,
        "cost_ratio": cost_ratio,
        "pilot": pilot,
        "promote": all(checks.values()),
        "runtime_ratio": runtime_ratio,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("baseline", type=Path)
    parser.add_argument("pilot", type=Path)
    parser.add_argument("--thresholds", type=Path, default=Path(__file__).with_name("thresholds.json"))
    args = parser.parse_args()
    try:
        baseline = score(json.loads(args.baseline.read_text(encoding="utf-8")))
        pilot = score(json.loads(args.pilot.read_text(encoding="utf-8")))
        thresholds = json.loads(args.thresholds.read_text(encoding="utf-8"))
        result = compare(baseline, pilot, thresholds)
    except (OSError, json.JSONDecodeError, ReportError, KeyError, TypeError, ValueError) as error:
        print(json.dumps({"error": str(error), "status": "error"}, sort_keys=True))
        return 2
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0 if result["promote"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
