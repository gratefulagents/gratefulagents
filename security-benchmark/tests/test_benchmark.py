from __future__ import annotations

import copy
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
REPOSITORY_ROOT = BENCHMARK_ROOT.parent
EVALUATOR = BENCHMARK_ROOT / "evaluator"
sys.path.insert(0, str(EVALUATOR))

import runner
import scoring


class BenchmarkTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manifest = runner.load_manifest()

    def test_manifest_has_requested_coverage_and_complete_private_metadata(self):
        cases = self.manifest["cases"]
        self.assertEqual(15, len(cases))
        self.assertEqual(15, len({case["id"] for case in cases}))
        requested = {
            "initialization and upgrade-state failures",
            "cross-chain origin, identity, nonce, and proof binding",
            "fork-versus-upstream validation divergence",
            "callback-visible accounting inconsistencies",
            "timeout, retry, refund, and recovery reentrancy/idempotency",
            "encoding, signature, and domain collisions",
            "decimal, scaling, and repeated-rounding errors",
            "alternate-path state-transition validation differences",
            "source/artifact/deployment mismatch",
            "realistic resource or liveness failures",
        }
        self.assertEqual(requested, {case["family"] for case in cases})
        for case in cases:
            for field in ("invariant", "attacker", "control", "guard", "applicability", "provenance"):
                self.assertIsInstance(case[field], str)
                self.assertTrue(case[field])
            self.assertGreaterEqual(len(case["sequence"]), 2)
            self.assertLessEqual(len(case["sequence"]), self.manifest["max_sequence_steps"])
            self.assertEqual({"candidate", "control", "mutation"}, set(case["snapshots"]))

    def test_all_snapshot_digests_are_pinned(self):
        for case in self.manifest["cases"]:
            self.assertEqual(set(case["snapshots"].values()), set(case["sha256"]))
            for filename, expected in case["sha256"].items():
                path = BENCHMARK_ROOT / "staged" / case["id"] / filename
                self.assertEqual(expected, hashlib.sha256(path.read_bytes()).hexdigest())

    def test_every_candidate_fails_control_passes_and_mutation_fails(self):
        for case in self.manifest["cases"]:
            with self.subTest(case=case["id"]):
                self.assertEqual(
                    {"candidate": False, "control": True, "mutation": False},
                    runner.evaluate_case(case),
                )

    def test_exact_runner_output_is_deterministic(self):
        expected = {
            "candidate_violations": 15,
            "cases": 15,
            "controls_safe": 15,
            "mutations_detected": 15,
            "seed": 320,
            "status": "pass",
        }
        self.assertEqual(expected, runner.run(320, 15))
        self.assertEqual(expected, runner.run(320, 15))
        environment = os.environ.copy()
        environment.update({"PYTHONHASHSEED": "0", "LC_ALL": "C", "TZ": "UTC"})
        completed = subprocess.run(
            [sys.executable, "security-benchmark/evaluator/runner.py", "--seed", "320", "--max-cases", "15"],
            cwd=REPOSITORY_ROOT,
            env=environment,
            check=False,
            capture_output=True,
            text=True,
            timeout=10,
        )
        self.assertEqual(0, completed.returncode, completed.stderr)
        self.assertEqual(
            '{"candidate_violations":15,"cases":15,"controls_safe":15,"mutations_detected":15,"seed":320,"status":"pass"}\n',
            completed.stdout,
        )

    def test_bounds_are_not_silently_changed(self):
        with self.assertRaisesRegex(runner.BenchmarkError, "seed must be 320"):
            runner.run(321, 15)
        with self.assertRaisesRegex(runner.BenchmarkError, "max-cases must be 15"):
            runner.run(320, 14)

    def test_snapshot_tampering_is_rejected_before_execution(self):
        case = self.manifest["cases"][0]
        filename = case["snapshots"]["candidate"]
        with tempfile.TemporaryDirectory() as temporary:
            fake_root = Path(temporary)
            target = fake_root / "staged" / case["id"] / filename
            target.parent.mkdir(parents=True)
            original = BENCHMARK_ROOT / "staged" / case["id"] / filename
            target.write_bytes(original.read_bytes() + b"\n")
            with mock.patch.object(runner, "BENCHMARK_ROOT", fake_root):
                with self.assertRaisesRegex(runner.BenchmarkError, "snapshot digest mismatch"):
                    runner.evaluate_case(case)

    def test_manifest_tampering_is_rejected_before_parsing(self):
        with tempfile.TemporaryDirectory() as temporary:
            manifest_path = Path(temporary) / "manifest.json"
            lock_path = Path(temporary) / "manifest.sha256"
            manifest_path.write_text("{}\n", encoding="utf-8")
            lock_path.write_text("0" * 64 + "\n", encoding="ascii")
            with mock.patch.object(runner, "MANIFEST_PATH", manifest_path), mock.patch.object(runner, "LOCK_PATH", lock_path):
                with self.assertRaisesRegex(runner.BenchmarkError, "manifest digest mismatch"):
                    runner.load_manifest()

    def test_staged_tree_contains_no_evaluator_metadata(self):
        staged = BENCHMARK_ROOT / "staged"
        self.assertFalse(list(staged.rglob("*.json")))
        snapshot_text = "\n".join(path.read_text(encoding="utf-8") for path in staged.rglob("*.py"))
        for case in self.manifest["cases"]:
            self.assertNotIn(case["family"], snapshot_text)
            self.assertNotIn(case["invariant"], snapshot_text)
            self.assertNotIn(case["attacker"], snapshot_text)
            self.assertNotIn(case["provenance"], snapshot_text)

    def test_blind_stage_exposes_one_neutral_snapshot_and_public_contract(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            stage = root / "agent-mount"
            assignment = root / "evaluator-only" / "assignment.json"
            result = runner.stage_blind(stage, assignment, 320)
            self.assertEqual("staged", result["status"])
            self.assertTrue(assignment.is_file())
            private_assignment = json.loads(assignment.read_text(encoding="utf-8"))
            self.assertEqual(64, len(private_assignment["selection_digest"]))
            for case in self.manifest["cases"]:
                exposed = stage / case["id"]
                self.assertEqual({"target.py", "input.json", "SPEC.md"}, {path.name for path in exposed.iterdir()})
                self.assertFalse(any("snapshot_" in path.name for path in exposed.iterdir()))
            self.assertFalse((stage / "evaluator").exists())
            self.assertFalse((stage / ".git").exists())

    def test_mutation_is_distinct_from_candidate_and_control(self):
        for case in self.manifest["cases"]:
            digests = [case["sha256"][case["snapshots"][role]] for role in ("candidate", "control", "mutation")]
            self.assertEqual(3, len(set(digests)), case["id"])


class ScoringTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manifest = runner.load_manifest()
        cls.ids = [case["id"] for case in cls.manifest["cases"]]

    def report(self, found_count: int, miss_reasons: tuple[str, ...], time: float, cost: float):
        cases = {}
        for index, case_id in enumerate(self.ids):
            entry = {"time_seconds": time, "cost_usd": cost, "duplicate_effort": index % 2}
            if index < found_count:
                entry.update(
                    {
                        "disposition": "found",
                        "evidence_complete": True,
                        "attacker_correct": True,
                        "invariant_correct": True,
                        "impact_correct": True,
                        "reproduced": True,
                        "root_cause_explanation": "Evaluator-confirmed root cause and violated property.",
                        "reproduction_artifact": f"artifacts/{case_id}.json",
                    }
                )
            else:
                entry.update(
                    {
                        "disposition": "missed",
                        "miss_reason": miss_reasons[(index - found_count) % len(miss_reasons)],
                    }
                )
            cases[case_id] = entry
        return {
            "benchmark_digest": runner.LOCK_PATH.read_text(encoding="ascii").strip(),
            "bounds": {"max_cases": 15, "max_sequence_steps": 3},
            "cases": cases,
            "fixed_controls": {case_id: "assessed_clean" for case_id in self.ids},
            "seed": 320,
            "selection_digest": "1" * 64,
            "workflow_revision": "test-revision",
        }

    def test_baseline_pilot_scoring_and_all_miss_categories(self):
        baseline_report = self.report(7, scoring.MISS_REASONS, 10, 1)
        pilot_report = self.report(13, scoring.MISS_REASONS, 12, 1.2)
        baseline = scoring.score(baseline_report, self.manifest)
        pilot = scoring.score(pilot_report, self.manifest)
        for reason in scoring.MISS_REASONS:
            self.assertEqual(1, baseline["miss_taxonomy"][reason])
        thresholds = json.loads((EVALUATOR / "thresholds.json").read_text(encoding="utf-8"))
        comparison = scoring.compare(baseline, pilot, thresholds)
        self.assertTrue(comparison["promote"])
        self.assertTrue(all(comparison["checks"].values()))
        self.assertEqual(1.2, comparison["runtime_ratio"])
        self.assertAlmostEqual(1.2, comparison["cost_ratio"])

    def test_threshold_failure_is_explicit(self):
        baseline = scoring.score(self.report(7, scoring.MISS_REASONS, 10, 1), self.manifest)
        pilot = scoring.score(self.report(13, scoring.MISS_REASONS, 12, 1.2), self.manifest)
        thresholds = json.loads((EVALUATOR / "thresholds.json").read_text(encoding="utf-8"))
        thresholds["minimum_pilot_recall"] = 1.0
        comparison = scoring.compare(baseline, pilot, thresholds)
        self.assertFalse(comparison["promote"])
        self.assertFalse(comparison["checks"]["minimum_pilot_recall"])

    def test_missing_case_and_unclassified_miss_are_rejected(self):
        report = self.report(7, scoring.MISS_REASONS, 10, 1)
        report["cases"].pop(self.ids[-1])
        with self.assertRaisesRegex(scoring.ReportError, "case coverage mismatch"):
            scoring.score(report, self.manifest)
        report = self.report(7, scoring.MISS_REASONS, 10, 1)
        missed_id = self.ids[7]
        report["cases"][missed_id].pop("miss_reason")
        with self.assertRaisesRegex(scoring.ReportError, "agreed miss_reason"):
            scoring.score(report, self.manifest)

    def test_resource_and_provenance_omissions_cannot_promote(self):
        for bad_value in (-1, float("nan"), "1", True):
            report = self.report(15, scoring.MISS_REASONS, 1, 0.1)
            report["cases"][self.ids[0]]["time_seconds"] = bad_value
            with self.assertRaises(scoring.ReportError):
                scoring.score(report, self.manifest)
        report = self.report(15, scoring.MISS_REASONS, 1, 0.1)
        report.pop("fixed_controls")
        with self.assertRaisesRegex(scoring.ReportError, "fixed_controls"):
            scoring.score(report, self.manifest)
        baseline = scoring.score(self.report(7, scoring.MISS_REASONS, 0, 0), self.manifest)
        pilot = scoring.score(self.report(13, scoring.MISS_REASONS, 1, 1), self.manifest)
        thresholds = json.loads((EVALUATOR / "thresholds.json").read_text(encoding="utf-8"))
        comparison = scoring.compare(baseline, pilot, thresholds)
        self.assertFalse(comparison["promote"])
        self.assertTrue(comparison["runtime_ratio"] == float("inf"))

    def test_fixed_false_positives_and_evidence_metrics_are_counted(self):
        report = self.report(15, scoring.MISS_REASONS, 1, 0.1)
        report["fixed_controls"][self.ids[0]] = "reported_vulnerable"
        report["fixed_controls"][self.ids[1]] = "reported_vulnerable"
        report["cases"][self.ids[0]]["evidence_complete"] = False
        report["cases"][self.ids[1]]["reproduced"] = False
        result = scoring.score(report, self.manifest)
        self.assertAlmostEqual(2 / 15, result["false_positive_rate"])
        self.assertAlmostEqual(59 / 60, result["evidence_completeness"])
        self.assertAlmostEqual(14 / 15, result["reproduction_rate"])


if __name__ == "__main__":
    unittest.main()
