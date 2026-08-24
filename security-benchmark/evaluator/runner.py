from __future__ import annotations

import argparse
import copy
import hashlib
import json
import random
from pathlib import Path
from typing import Any

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
MANIFEST_PATH = Path(__file__).with_name("manifest.json")
LOCK_PATH = Path(__file__).with_name("manifest.sha256")


class BenchmarkError(RuntimeError):
    pass


def _digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def load_manifest() -> dict[str, Any]:
    manifest_bytes = MANIFEST_PATH.read_bytes()
    expected = LOCK_PATH.read_text(encoding="ascii").strip()
    observed = _digest(manifest_bytes)
    if observed != expected:
        raise BenchmarkError(f"manifest digest mismatch: expected {expected}, observed {observed}")
    return json.loads(manifest_bytes)


def _load_snapshot(case_id: str, filename: str, expected_digest: str):
    case_root = (BENCHMARK_ROOT / "staged" / case_id).resolve()
    path = (case_root / filename).resolve()
    if path.parent != case_root or path.suffix != ".py":
        raise BenchmarkError(f"snapshot path escapes case directory: {case_id}/{filename}")
    source = path.read_bytes()
    observed = _digest(source)
    if observed != expected_digest:
        raise BenchmarkError(f"snapshot digest mismatch for {case_id}/{filename}: expected {expected_digest}, observed {observed}")
    namespace: dict[str, Any] = {"__builtins__": __builtins__, "__name__": f"snapshot_{case_id}"}
    exec(compile(source, str(path), "exec"), namespace)
    execute = namespace.get("execute")
    if not callable(execute):
        raise BenchmarkError(f"snapshot {case_id}/{filename} has no execute function")
    return execute


def _oracle_holds(observed: dict[str, Any], oracle: dict[str, Any]) -> bool:
    if oracle["field"] not in observed:
        raise BenchmarkError(f"oracle field {oracle['field']} is absent")
    actual = observed[oracle["field"]]
    if oracle["op"] == "eq":
        return actual == oracle["value"]
    if oracle["op"] == "le":
        return actual <= oracle["value"]
    raise BenchmarkError(f"unsupported oracle operation {oracle['op']}")


def evaluate_case(case: dict[str, Any]) -> dict[str, bool]:
    outcomes = {}
    for role, filename in case["snapshots"].items():
        execute = _load_snapshot(case["id"], filename, case["sha256"][filename])
        observed = execute(copy.deepcopy(case["input"]))
        if not isinstance(observed, dict):
            raise BenchmarkError(f"snapshot {case['id']}/{filename} returned a non-object")
        outcomes[role] = _oracle_holds(observed, case["oracle"])
    if "positive_input" in case:
        filename = case["snapshots"]["control"]
        execute = _load_snapshot(case["id"], filename, case["sha256"][filename])
        positive = execute(copy.deepcopy(case["positive_input"]))
        if positive.get("executed") is not True or positive.get("rejected") is not False:
            raise BenchmarkError(f"positive control is vacuous for {case['id']}")
    return outcomes


def run(seed: int = 320, max_cases: int = 15) -> dict[str, Any]:
    manifest = load_manifest()
    if seed != manifest["seed"]:
        raise BenchmarkError(f"seed must be {manifest['seed']}")
    if max_cases != manifest["max_cases"]:
        raise BenchmarkError(f"max-cases must be {manifest['max_cases']}")
    cases = list(manifest["cases"])
    random.Random(seed).shuffle(cases)
    cases = cases[:max_cases]
    results = [evaluate_case(case) for case in cases]
    candidate_violations = sum(not result["candidate"] for result in results)
    controls_safe = sum(result["control"] for result in results)
    mutations_detected = sum(not result["mutation"] for result in results)
    passed = candidate_violations == controls_safe == mutations_detected == len(cases)
    return {
        "candidate_violations": candidate_violations,
        "cases": len(cases),
        "controls_safe": controls_safe,
        "mutations_detected": mutations_detected,
        "seed": seed,
        "status": "pass" if passed else "fail",
    }


def stage_blind(
    stage_dir: Path,
    assignment_file: Path,
    seed: int = 320,
    snapshot_role: str = "candidate",
) -> dict[str, Any]:
    """Build one role-neutral agent mount for discovery or fixed-control scoring."""
    manifest = load_manifest()
    if seed != manifest["seed"]:
        raise BenchmarkError(f"seed must be {manifest['seed']}")
    if snapshot_role not in ("candidate", "control"):
        raise BenchmarkError("snapshot-role must be candidate or control")
    if stage_dir.exists():
        raise BenchmarkError(f"stage directory already exists: {stage_dir}")
    if assignment_file.resolve().is_relative_to(stage_dir.resolve()):
        raise BenchmarkError("assignment file must remain outside the agent stage")
    stage_dir.mkdir(parents=True)
    assignments: dict[str, Any] = {
        "benchmark_digest": LOCK_PATH.read_text(encoding="ascii").strip(),
        "cases": {},
        "seed": seed,
    }
    for case in manifest["cases"]:
        role = snapshot_role
        filename = case["snapshots"][role]
        case_root = (BENCHMARK_ROOT / "staged" / case["id"]).resolve()
        source = (case_root / filename).resolve()
        if source.parent != case_root or source.suffix != ".py":
            raise BenchmarkError(f"snapshot path escapes case directory: {case['id']}/{filename}")
        source_bytes = source.read_bytes()
        if _digest(source_bytes) != case["sha256"][filename]:
            raise BenchmarkError(f"snapshot digest mismatch for {case['id']}/{filename}")
        target_dir = stage_dir / case["id"]
        target_dir.mkdir()
        (target_dir / "target.py").write_bytes(source_bytes)
        (target_dir / "input.json").write_text(json.dumps(case["input"], sort_keys=True) + "\n", encoding="utf-8")
        (target_dir / "SPEC.md").write_text(
            "# Public behavior contract\n\n"
            f"Expected safety property: {case['invariant']}\n\n"
            f"Expected guard: {case['guard']}\n\n"
            "Call `execute(data)` in `target.py` with `input.json`. Determine whether this snapshot "
            "preserves the contract without assuming whether it is vulnerable or fixed.\n",
            encoding="utf-8",
        )
        assignments["cases"][case["id"]] = {"role": role, "source_sha256": case["sha256"][filename]}
    assignments["selection_digest"] = _digest(
        json.dumps(assignments["cases"], sort_keys=True, separators=(",", ":")).encode("utf-8")
    )
    assignment_file.parent.mkdir(parents=True, exist_ok=True)
    assignment_file.write_text(json.dumps(assignments, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return {"cases": len(manifest["cases"]), "seed": seed, "stage": str(stage_dir), "status": "staged"}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--seed", type=int, default=320)
    parser.add_argument("--max-cases", type=int, default=15)
    parser.add_argument("--stage-dir", type=Path)
    parser.add_argument("--assignment-file", type=Path)
    parser.add_argument("--snapshot-role", choices=("candidate", "control"), default="candidate")
    args = parser.parse_args()
    try:
        if args.stage_dir:
            if not args.assignment_file:
                raise BenchmarkError("--assignment-file is required with --stage-dir")
            result = stage_blind(args.stage_dir, args.assignment_file, args.seed, args.snapshot_role)
        else:
            if args.assignment_file:
                raise BenchmarkError("--assignment-file requires --stage-dir")
            result = run(args.seed, args.max_cases)
    except (BenchmarkError, OSError, ValueError, TypeError, SyntaxError) as error:
        print(json.dumps({"error": str(error), "status": "error"}, sort_keys=True))
        return 2
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0 if result["status"] in ("pass", "staged") else 1


if __name__ == "__main__":
    raise SystemExit(main())
