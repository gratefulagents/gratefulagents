# Staged snapshots

Each `bx-*` directory is a role-neutral benchmark snapshot set. Files deliberately use opaque identifiers and the same `execute(data)` interface. This tree contains no weakness family, expected result, invariant, provenance, or role mapping.

This repository tree is evaluator input, not an agent mount. Use `runner.py --snapshot-role candidate --stage-dir <discovery-dir> --assignment-file <outside-dir>/discovery.json` for recall, then a separate equally blind run with `--snapshot-role control` for false positives. Each command copies exactly one snapshot per case under the neutral name `target.py` and adds only its public contract/input. Mount only that generated directory. The evaluator privately retains the role assignment, oracle, exploit sequence, and provenance. Presenting this three-snapshot source tree or `../evaluator/` to an agent would turn discovery into differential matching and is not a valid blind run.
