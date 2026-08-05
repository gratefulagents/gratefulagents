---
title: Security scanning
seoTitle: Autonomous AI Security Scans with SecurityScan | GratefulAgents
description: Run one-shot or scheduled AI security scans against a repository with the SecurityScan resource, triage deduplicated findings, and export Markdown and SARIF reports.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/projects/security-scanning/ and help me configure a SecurityScan for my repository, including scope, workflow tasks, ranking rules, and how to triage the findings.
---

# Security scanning

A **SecurityScan** is a cluster resource that runs an autonomous, agent-driven security review of a git repository. Each scan creates an agent run that fans out focused vulnerability-hunting sub-agents, collects structured findings, deduplicates and ranks them, and produces a Markdown report and a SARIF file. Scans run once per spec change or on a cron schedule.

You create and edit SecurityScans from the dashboard's **Security** pages (**Scan configurations** → **New scan**), or with `kubectl apply` if you prefer manifests. Findings and reports appear on the same pages and in the resource's status.

## What a scan is — and is not

Understand the trust boundaries before acting on results:

- **Scanned code is untrusted input.** The scan reads the target repository, and everything a finding contains — titles, descriptions, code snippets — is derived from that repository by a model. Treat finding text as untrusted when you copy it into tickets, chat, or other systems.
- **Scans are read-only research.** Scan runs use the `security-scan` mode by default, which clamps the run to read-only, and workflow tasks assume read-only specialist roles. The finding tools only write scan state on the platform; a scan does not modify the repository. The one exception is the `exploit-validator` role, which may build a proof of concept inside the sandbox — never against live or third-party systems.
- **Findings are AI-generated.** Expect false positives and missed issues. A scan is a research aid that points humans at suspicious code; it is not a compliance scanner, and its findings require human validation before you treat them as real vulnerabilities or disclose them.
- **Sandboxing is required.** A scan whose `spec.defaults` disables the command sandbox or requests Kubernetes-admin access is rejected with `Ready=False` and reason `InsecureDefaults`.

## A minimal one-shot scan

With no `schedule`, the scan runs exactly once per spec generation: the controller creates one agent run, and creates another only when you change the spec.

```yaml
apiVersion: triggers.gratefulagents.dev/v1alpha1
kind: SecurityScan
metadata:
  name: webapp-scan
spec:
  repoURL: https://github.com/example/webapp.git
  baseBranch: main          # default "main"
  defaults:                 # AgentRun defaults; repoURL and baseBranch are forced from the scan target
    model: gpt-5.4
    provider: openai
    secrets:
      githubToken: github-token
      providerKeys:
        - provider: openai
          secretName: openai-api-key
          secretKey: api-key
```

Everything else — workflow, parallelism, dedupe, severity threshold — uses the defaults described below.

## A complete scheduled scan

```yaml
apiVersion: triggers.gratefulagents.dev/v1alpha1
kind: SecurityScan
metadata:
  name: webapp-weekly-scan
spec:
  # Target
  repoURL: https://github.com/example/webapp.git
  baseBranch: main                    # default "main"
  revision: ""                        # optional commit pin, checked out in the run sandbox; empty = head of baseBranch at scan time
  additionalRepos:                    # dependency repos cloned and scanned alongside the target
    - https://github.com/example/webapp-sdk.git

  # Scope: narrow what the scan looks at
  scope:
    focus: "Prioritize the payment and authentication flows."
    includePaths: ["src/**", "api/**"]   # only scan these globs when non-empty
    excludePaths: ["**/testdata/**"]     # globs the scan should skip
    languages: ["go", "typescript"]      # restrict analysis to these languages

  # Workflow: the sub-agent research plan. Omit to use the built-in default workflow.
  workflow:
    - name: recon
      objective: "Map entry points, trust boundaries, and data flows from untrusted input to sensitive sinks."
      category: recon
    - name: injection-hunt
      objective: "Hunt for SQL/command/template injection and unsanitized input reaching interpreters."
      category: injection
      dependsOn: [recon]                # waits until "recon" completes
      role: security-reviewer           # RoleInstruction for this task's sub-agent (default "security-reviewer")
      model: claude-opus-4-6            # optional per-task model override
      maxFindings: 20                   # cap findings from this task; 0 or omitted = unlimited
    - name: authz-hunt
      objective: "Hunt for missing ownership checks, IDOR, and cross-tenant leakage."
      category: authz
      dependsOn: [recon]
    - name: triage-and-report
      objective: "Verify exploitability, drop false positives, apply the ranking rules, then submit the final report."
      category: triage
      dependsOn: [injection-hunt, authz-hunt]

  # At most this many workflow tasks run concurrently. Default 4, range 1-16.
  parallelism: 4

  # Operator-authored ranking rules, injected into the scan prompt and applied
  # to the final report. Directives are described under "Ranking rules" below.
  severityRankers:
    - name: internal-policy
      rules: |
        severity-floor: injection=high
        exclude: info-leak
        min-severity: medium
        weight: exploitability=0.3,severity=0.4
        Anything reachable without authentication outranks authenticated issues.

  # Per-finding follow-up prompts executed after the research tasks complete.
  postScripts:
    - name: validate
      prompt: "Re-read the cited code and confirm the finding is exploitable; downgrade it if not."
      runOn: high-and-above             # one of: all (default), confirmed, high-and-above

  # Duplicate suppression. This policy is enforced when the report is submitted.
  dedupe:
    enabled: true                       # default true
    similarityThresholdPermille: 820    # default 820 = findings with similarity >= 0.82 merge

  # Findings below this severity are excluded from the report. The controller
  # stamps this policy on the run and the report tool enforces it. Default "low".
  minSeverity: low

  # When set, the scan's Ready condition becomes False with reason
  # FindingsExceedThreshold while OPEN findings at or above this severity exist.
  failOnSeverity: high

  # Schedule: standard 5-field cron or a descriptor such as "@daily".
  # Omit to run once per spec generation instead.
  schedule: "0 3 * * 1"
  timeZone: UTC                         # IANA time zone, default UTC
  suspend: false                        # pause new scan runs while keeping status readable
  concurrencyPolicy: Forbid             # Forbid (default) skips a tick while a previous run is active; Allow overlaps

  # Runtime cap for each scan run; overrides defaults.timeout when set.
  maxRuntime: 2h

  # AgentRun defaults for scan runs (model, provider, secrets, runtime, and so on).
  # The controller forces defaults.repoURL and defaults.baseBranch from the scan
  # target, and applies the "security-scan" mode unless defaults.modeRef is set.
  # disableCommandSandbox and kubernetesAdmin are not allowed for SecurityScans.
  defaults:
    model: gpt-5.4
    provider: openai
    secrets:
      githubToken: github-token
      providerKeys:
        - provider: openai
          secretName: openai-api-key
          secretKey: api-key
```

## How a scan runs

The controller seeds each scan run with a generated prompt containing the target, scope, the workflow as an explicit sub-agent plan, the machine-readable finding contract, ranking rules, and the reporting policy. The coordinating agent then spawns **one sub-agent per workflow task**, runs tasks whose dependencies are complete in parallel (never more than `parallelism` at a time), and holds back a task until everything in its `dependsOn` list has finished.

When `workflow` is empty, the built-in default plan is used: eleven focused hunting tasks plus a final triage task that depends on all of them.

| Task | Category | Role |
| --- | --- | --- |
| `attack-surface-mapping` | recon | `threat-modeler` |
| `authn-authz` | authn | `vulnerability-hunter` |
| `injection-and-input-handling` | injection | `vulnerability-hunter` |
| `secrets-and-credentials` | secrets | `secrets-auditor` |
| `crypto-and-randomness` | crypto | `vulnerability-hunter` |
| `ssrf-and-network` | ssrf | `vulnerability-hunter` |
| `deserialization-and-parsing` | deserialization | `vulnerability-hunter` |
| `access-control-and-multitenancy` | authz | `vulnerability-hunter` |
| `dependency-and-supply-chain` | supply-chain | `dependency-auditor` |
| `infrastructure-and-configuration` | misconfiguration | `vulnerability-hunter` |
| `business-logic` | logic-flaw | `vulnerability-hunter` |
| `triage-and-report` | triage (depends on all of the above) | `finding-triager` |

### Specialist roles and the scan skill

The platform ships six `RoleInstruction` specialists used by the default plan — `threat-modeler`, `vulnerability-hunter`, `secrets-auditor`, `dependency-auditor`, `exploit-validator`, and `finding-triager` — plus a `security-scan` `Skill` containing the scanning handbook (per-language sources and sinks, framework pitfalls, authn/authz and injection checklists, IaC/CI misconfiguration checks, and severity calibration). The `security-scan` mode attaches the skill to every scan run, and the coordinating agent loads it on demand.

Set `role` on a workflow task to pick a different specialist, or define your own `RoleInstruction` and reference it by name.

Sub-agents report vulnerabilities by calling the `report_security_finding` tool with one structured finding at a time; findings described only in prose are not recorded. During triage the agent can use `list_security_findings` to review what was reported and `update_security_finding` to change a finding's status with a note. The run finishes by calling `submit_security_scan_report` exactly once, which enforces the scan's dedupe and minimum-severity policies, ranks findings, renders the report artifacts, and marks the scan completed.

## Findings

A finding is a structured record. Required fields are `title`, `category`, `severity`, and `description`; the rest add location and evidence:

| Field | Meaning |
| --- | --- |
| `title` | Short, specific summary of the issue. |
| `category` | One of `injection`, `authn`, `authz`, `secrets`, `crypto`, `ssrf`, `xss`, `deserialization`, `path-traversal`, `race-condition`, `dos`, `memory-safety`, `supply-chain`, `misconfiguration`, `logic-flaw`, `info-leak`, `other`. |
| `severity` | `critical`, `high`, `medium`, `low`, or `info`. |
| `confidence` | `confirmed`, `firm`, or `tentative` (default). |
| `file_path`, `start_line`, `end_line`, `symbol` | Where the vulnerable code lives. |
| `cwe` | CWE identifiers such as `CWE-89`. |
| `description`, `impact`, `attack_vector`, `remediation` | Why it is exploitable, what an attacker gains, how it is reached, and how to fix it. |
| `evidence` | Code citations: `file_path`, `start_line`, `end_line`, `snippet`, `note`. |
| `references`, `tags` | External references and free-form labels. |

The platform stamps a finding's repository and revision and anchors source-agent identity to the run, so the agent cannot forge its provenance. It accepts only well-formed `http` and `https` reference URLs, and redacts obvious credential material from finding prose and evidence snippets before storage.

Every finding gets a stable **fingerprint** derived from its category, location, and normalized title, so the same issue reported twice merges instead of duplicating.

### Lifecycle

Findings start as **open** and move through a triage lifecycle; every status change records an audit event with an actor and note:

`open` → `triaged` / `confirmed` / `false_positive` / `fixed` / `accepted_risk`

Change a finding's status from the dashboard, or from inside an agent run with the `update_security_finding` tool.

### Dedupe

Findings persist across runs of the same scan. Their storage key is `(namespace, scan name, repository, fingerprint)`, so re-observing a fingerprint in a later run increments its occurrence count and preserves its triage status. Finding and status counts are scoped to the scan across all of its runs.

At report submission, the controller-stamped dedupe policy controls similarity suppression rather than only guiding the agent. Findings with the same fingerprint always merge; beyond that, findings merge when similarity meets the configured threshold (default 0.82; set `spec.dedupe.similarityThresholdPermille` to tune). Set `spec.dedupe.enabled: false` to stamp dedupe permille `0` and disable report-time dedupe. Similarity compares title and description tokens, boosted when findings sit in the same file with overlapping line ranges or share a CWE. The surviving canonical finding of each cluster is the one with the highest severity, then highest confidence, then most evidence, then longest description.

### Ranking

The final report scores every finding from 0 to 100 by weighing severity (0.5), confidence (0.2), exploitability (0.15), and exposure (0.15), and orders the report by score. Steer scoring with **ranker rules** in `spec.severityRankers[].rules`. Recognized directives, one per line and case-insensitive:

```text
severity-floor: <category>=<severity>
severity-ceiling: <category>=<severity>
exclude: <category>[,<category>...]
min-severity: <severity>
weight: <severity|confidence|exploitability|exposure>=<float>[,<name>=<float>...]
```

- `severity-floor` raises every finding of a category to at least the given severity; floors apply before the `min-severity` filter, so a floored finding cannot be filtered out by it.
- `severity-ceiling` caps a category's severity.
- `exclude` drops whole categories from the report.
- `min-severity` filters findings below a severity. The report tool always applies the stricter of this model-supplied value and the controller-stamped `spec.minSeverity` policy.
- `weight` rebalances the four scoring dimensions.

Lines that are not valid directives are kept as prose and given to the triage agent verbatim, so you can mix directives with plain-language ranking guidance.

## Reports, status, and the dashboard

When the scan submits its report, two artifacts are saved on the scan's agent run:

- **`security_report`** — a Markdown report with the executive summary and ranked findings.
- **`security_sarif`** — a SARIF 2.1.0 file suitable for importing into code-scanning tools; each result carries the finding fingerprint for cross-referencing.

In the dashboard, the **Security** section lists scans with their status and per-severity finding counts. Each scan links to a detail page where you can filter findings by severity, status, category, and text search, and change a finding's status inline — for example, marking a validated non-issue as `false_positive` or a real one as `confirmed`.

On the cluster side, `kubectl get securityscans` shows the repository, schedule, last scan time, and critical/high/total finding counts. The resource status also records the last run name, next scheduled time, cumulative runs created, scan-scoped finding counts, and a `Ready` condition. With `failOnSeverity` set, `Ready` turns `False` with reason `FindingsExceedThreshold` while open scan findings at or above that severity exist — useful for alerting on scan results.

## Scheduling behavior

- **One-shot** (no `schedule`): the scan runs when created and again whenever the spec generation changes.
- **Scheduled**: standard five-field cron expressions and descriptors such as `@daily` are supported, evaluated in `timeZone` (default UTC). The default `concurrencyPolicy` is `Forbid`: a due tick is skipped while a previous scan run is still active. Set `Allow` to permit overlapping runs. When a scheduled run finishes, the scan phase becomes `Completed` until the next run starts.
- **Suspend**: set `spec.suspend: true` to pause new scan runs without deleting the resource; status stays readable.

A malformed schedule sets `Ready=False` with reason `InvalidSchedule`. A schedule that parses but cannot produce a next time sets `Ready=False` with reason `ScheduleError` instead of repeatedly reconciling it.

Skipped ticks are not backfilled, matching [Cron schedule](./cron.md) semantics.

**Deletion.** Deleting a SecurityScan removes its stored scans, findings, and triage history: the controller holds a cleanup finalizer and purges the persisted data before the resource disappears. If the findings store is unreachable, deletion is retried with backoff and released after a bounded grace period so a failing store cannot wedge the resource; the run artifacts (Markdown report and SARIF) live with their agent runs and follow the run's own lifecycle.

## Operational guidance

**Cost and models.** A scan is expensive relative to a normal run: every workflow task is its own sub-agent that reads the repository, so cost scales with the number of tasks, repository size, and model choice. The default workflow launches twelve sub-agents. To control spend: narrow `scope.includePaths` and `scope.languages`, replace the default workflow with fewer targeted tasks, cap noisy tasks with `maxFindings`, use a cheaper `defaults.model` and reserve a stronger per-task `model` override for the triage task, and set `maxRuntime` as a hard stop.

**Parallelism.** `parallelism` (default 4) trades wall-clock time against concurrent load: each in-flight task is a live sub-agent consuming provider rate limits and sandbox resources. Raise it toward 16 for wide, independent workflows when your provider limits allow; lower it to 1–2 on constrained clusters or strict rate limits.

**Triage discipline.** Treat a fresh scan as a candidate list, not a report you forward. Validate each finding against the cited evidence, mark non-issues `false_positive` (the audit trail keeps them from being re-litigated on the next scan — reported duplicates merge into the existing finding instead of reopening it), mark real issues `confirmed`, and move them to `fixed` or `accepted_risk` once resolved. Changing a false positive, or any other finding, away from `open` updates the scan's open counts and clears its contribution to `failOnSeverity`. Use `postScripts` with `runOn: high-and-above` to make the scan itself re-verify its most important findings before they reach you, and ranker `exclude:` directives to silence categories that are consistently noise in your codebase.
