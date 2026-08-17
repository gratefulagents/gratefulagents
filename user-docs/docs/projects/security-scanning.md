---
title: Security scanning
seoTitle: Autonomous AI Security Scans with SecurityScan | GratefulAgents
description: Run one-shot or scheduled AI security scans with the SecurityScan resource, triage deduplicated findings, and export Markdown and SARIF reports.
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
      runOn: high-and-above             # one of: all (default), confirmed, high-and-above, high-and-above-actionable

  # Matching is evaluated once against each finding as research ends. All
  # matching post-scripts for that finding then normally run in order in one
  # AgentRun, so N findings create N follow-up runs rather than N × scripts.
  # Exceptionally large combined prompts split safely at script boundaries.

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
  # disableCommandSandbox, kubernetesAdmin, and dockerInDocker are not allowed for SecurityScans.
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

## Reusable security resources

Workflows, severity rankers, post-scripts, and bounty program scope snapshots can be shared across scans as namespace-scoped resources instead of being repeated inline in every `SecurityScan`:

- **`SecurityWorkflow`** — `spec.description`, `spec.tasks` (the same task schema as `spec.workflow`), and an optional `spec.parallelism` that overrides the referencing scan's parallelism when set.
- **`SecurityRanker`** — `spec.description` and `spec.rules`, a list of ranking rule lines in the same language as `spec.severityRankers[].rules`.
- **`SecurityPostScript`** — `spec.description`, `spec.prompt`, and `spec.runOn` (`all`, `confirmed`, `high-and-above`, or `high-and-above-actionable`). The actionable variant is for proof or remediation stages that should not start after a successful predecessor has already rejected, fixed, or accepted the risk; final reporting and audit stages should use `all` so they can record the terminal outcome.
- **`SecurityProgram`** — an operator-verified bounty or disclosure-program snapshot: provider, display name, HTTPS provenance URL, the explicit scope policy, when that policy was verified, and optional `scanTargets` for every independently importable repository. Each target selects its own default branch, workflow, policy pack, scan name, and catalog priority. The controller never fetches the program URL. Neither the URL nor a scan target authorizes network access; only `spec.scope.authorizedNetworkTargets` can do that.

### URL-driven web assessment workflows

For a website or domain target, choose the workflow by depth:

- `web-recon-passive` is the fast attack-surface inventory.
- `web-app-full-assessment` is the professional multi-track assessment based on OWASP WSTG/ASVS coverage: mapping fans out into authentication/session, authorization and tenant isolation, server-side input handling, browser/client security, API/GraphQL/WebSocket security, business logic, SSRF/file/parser integrations, and deployment/exposure testing before validation and triage.
- Focused workflows (`web-auth-session-assessment`, `web-access-control-assessment`, `web-api-assessment`, `web-client-side-assessment`, `web-server-side-input-assessment`, `web-business-logic-assessment`, and `web-deployment-exposure-assessment`) trade breadth for a deeper pass over one surface.
- `web-retest-confirmed-findings` rechecks previously reported issues without rerunning the entire assessment.

Website scans use dedicated full-access mode templates and do not remove Browser, WebFetch, Bash/CLI, registered scanners, workspace, or other tools granted by the selected RuntimeProfile. The workflow objectives carry the live-testing rule as an instruction: do not make stateful target changes, send potentially mutating requests, upload content, trigger callbacks, brute-force credentials, or perform load testing. When a valid test inherently requires a state change, the agent records it as untested unless the operator explicitly supplied a disposable fixture or environment; it must never simulate a successful result.

A `SecurityScan` references them with:

```yaml
spec:
  securityProgramRef:
    name: rootstock-immunefi       # quoted eligibility policy for every task
  workflowRef:
    name: payments-deep-dive       # replaces an inline workflow
  rankerRefs:
    - name: payments-priorities    # appended after spec.severityRankers
  postScriptRefs:
    - name: write-poc              # appended after spec.postScripts
```

### Precedence and override rules

- Inline fields keep working unchanged; existing specs need no migration.
- `workflowRef` and an inline `workflow` are **mutually exclusive**. Setting both makes the controller report `Ready=False` with reason `InvalidSpec` and never create a run; the dashboard rejects the combination at save time.
- `rankerRefs` and `postScriptRefs` **append** to the inline `severityRankers` and `postScripts` — inline entries come first, referenced entries follow in spec order.
- A referenced `SecurityWorkflow` with `spec.parallelism` set overrides the scan's own `parallelism` for runs built from it.
- A `SecurityProgram` URL without an explicit operator-verified `scopePolicy` is invalid. Eligibility checks fail closed when the reference is missing, stale, or not ready.

For example:

```yaml
apiVersion: triggers.gratefulagents.dev/v1alpha1
kind: SecurityProgram
metadata:
  name: acme-bounty
spec:
  provider: Example
  displayName: Acme Bug Bounty
  programURL: https://security.example.com/bug-bounty
  verifiedAt: "2026-08-11T00:00:00Z"
  scanTargets:
    - repositoryURL: https://github.com/example/acme-protocol
      baseBranch: main
      workflowRef: blockchain-protocol-audit
      policyPackRef: bug-bounty
      scanName: acme-protocol
      displayName: Acme protocol
      priority: 10
      featured: true
    - repositoryURL: https://github.com/example/acme-contracts
      baseBranch: develop
      workflowRef: bounty-hunt-evm
      policyPackRef: bug-bounty
      scanName: acme-contracts
      displayName: Acme contracts
      priority: 20
      featured: false
  scopePolicy: |
    Paste the operator-verified in-scope assets, eligible vulnerability
    classes, exclusions, testing restrictions, and reward requirements here.
```

`scanTargets` is optional and may contain up to 256 repositories. Scan names must be unique within the program. The deprecated singular `scanTarget` field is still read for existing resources, but it cannot be combined with `scanTargets`; editing a legacy program in the dashboard migrates it to the list form.

Program-page text is quoted to agents as untrusted policy data. Embedded instructions are not executed, and the URL is retained only as provenance. Update `scopePolicy` and `verifiedAt` after reviewing a changed program page; new executions receive the new snapshot while active and historical executions retain the old one.

### Snapshot semantics (reproducible runs)

References are resolved when each run is **created**, not when it executes or when you read it later. The controller inlines the referenced content into the run's seed prompt (which is persisted at creation), stamps the run with the `security.gratefulagents.dev/resolved-refs` annotation — a JSON array of `{kind, name, generation, hash}` records with a sha256 content hash of each resolved spec — and records the same snapshot in the scan's `status.lastResolvedRefs`. Editing a library resource afterwards changes only future runs; historical runs keep the exact content they were created with.

### Deleting referenced resources

Deleting a workflow, ranker, post-script, or security program through the dashboard is **blocked** while any `SecurityScan` in the namespace still references it; the error lists the referencing scans so you can detach them first. `kubectl delete` cannot be blocked (there is no admission webhook), so a kubectl-deleted resource leaves referencing scans reporting `Ready=False` with reason `UnresolvedReference` at their next run — no run is created until the reference is fixed. Runs that already happened are unaffected either way, because they carry their own snapshot.

### The library and the visual workflow builder

The dashboard's **Security → Library** page (`/security/library`) lists workflows, rankers, post-scripts, and programs with usage counts, and supports create, edit, duplicate where applicable, and guarded delete. Workflows are edited in a visual builder: structured task cards (name, objective, category, specialist role picker, model override, max findings), dependency selection limited to the other task names, and a live read-only graph of the dependency DAG. The builder refuses to save cycles, dangling or self dependencies, duplicate names, invalid roles/models, or an empty workflow — the same validation the server enforces on create/update and exposes through the `ValidateSecurityWorkflow` RPC. The scan form's *Workflow tasks* section lets you pick a library workflow (or keep editing inline), its *Rankers & post-scripts* section attaches library rankers and post-scripts, and its *Scope* section attaches an optional security program.

### AI-assisted authoring

Both the workflow and the post-script tabs of the library offer **Generate with AI**. Describe what you want in plain language and the platform launches a *bounded, repo-less* generation run: it clones nothing, receives no GitHub token, uses only your own saved provider credentials, and runs under the dedicated `security-draft` mode template with a short runtime cap. Your request text is passed as untrusted data — it cannot change the run's rules, tools, or output contract.

When the run finishes, the platform extracts the single JSON draft it produced, parses it defensively, and validates it with exactly the same rules manual authoring uses. The draft then opens in the normal editor with a review banner and any validation errors listed. Nothing is persisted server-side: the draft exists only in your browser until you save it through the regular create flow, which validates it again.

### Import and export (security packs)

**Export pack** serializes selected workflows, rankers, post-scripts, and scan configurations into a single JSON document:

```json
{
  "schemaVersion": "security-pack/v1",
  "exportedAt": "2026-01-01T00:00:00Z",
  "exportedBy": "alice",
  "sourceNamespace": "user-alice",
  "items": [{ "kind": "SecurityWorkflow", "name": "payments-deep-dive", "spec": { } }]
}
```

Packs never carry credentials. Secret references (`defaults.secrets`, provider keys, OAuth secret names) and the admin-only escape hatches (`kubernetesAdmin`, `disableCommandSandbox`, `dockerInDocker`) are stripped on export — and stripped again on import as defense in depth — so an imported scan configuration must be given its own credentials before it can run.

**Import pack** accepts a document of at most 1 MiB and up to 200 items. Import always starts as a **dry run**: every item is validated exactly like manual authoring and the per-item outcome (`would-create`, `skipped`, `renamed`, `failed` with field errors) is shown before anything is created. Choose what happens when a name already exists — fail the item, skip it, or import it under a new name — then apply. Imported scan configurations are owned by the importer, exactly like scans created through the dashboard.

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

### Triage & collaboration

Findings carry collaboration state beyond the status itself, and every change is an audited event in the finding's history:

- **Assignee** — assign a finding to a teammate (or clear it) from the finding page or in bulk from the scan detail table; the findings table can be filtered by assignee.
- **Bulk triage** — select multiple findings in the scan detail table and apply a status change (with an audit note) or an assignee in one operation. The batch is atomic: either every selected finding is updated or none is, and the result reports per-finding outcomes, so a stale selection can never half-apply.
- **Accepted-risk expiry** — when accepting a risk you can set an optional expiry. Past the deadline the finding automatically reopens (`accepted_risk_expired` event); the table shows an "expires in Xd" / "expired" badge while the acceptance is in effect.
- **Tickets** — link a finding to an external tracker issue (any `http(s)` URL, labeled with a provider such as GitHub or Linear), or create a GitHub issue directly through a `GitHubRepository` configured in the same namespace. Created issues carry only the finding's title, severity, category, location, and a link back to the dashboard — never raw evidence, impact analysis, or attack-vector text. Linear is link-only: create the issue in Linear and paste its URL. Linking and unlinking are audited (`ticket_linked` / `ticket_unlinked`).
- **Saved views** — save the current filter combination (severity, status, category, search, baseline state, assignee) under a name and re-apply it later. Saved views are private to the user who created them within a namespace.
- **Audit export** — download every audit event for a scan's findings (status changes, comments, assignments, ticket links, baseline transitions) as CSV or JSON from the scan detail page.

### Baselines

Every finding observation is recorded per scan run, which lets consecutive runs of the same scan be compared deterministically. Each finding carries a **baseline state**:

| State | Meaning |
| --- | --- |
| `new` | First observed by the latest run. |
| `recurring` | Observed by the previous run and again by the latest one. |
| `regressed` | Was suppressed (`fixed`, or `false_positive`/`accepted_risk` with a severity increase) and the evidence reappeared; the finding reopens automatically. |
| `resolved` | Present in an earlier run but absent from the latest successfully completed run. Nothing is deleted; `resolved_at` is stamped and a `resolved` event recorded. |
| `reopened` | A resolved finding whose evidence reappeared in a later run. |

Baseline resolution happens only when a scan run terminates **successfully** and has submitted its report — a failed or cancelled run never marks findings resolved. Suppression is sticky for `false_positive` and `accepted_risk` findings as long as the evidence is unchanged (the fingerprint pins evidence and location identity); a severity increase under the same fingerprint regresses the suppression to `open`, preserving the prior decision in the audit history. `fixed` findings always regress when they reappear.

The security overview shows namespace-wide baseline deltas (new / recurring / regressed / reopened / resolved) once observation data exists, plus **trend metrics**: average and median time-to-triage (first status change out of `open`) and time-to-resolution (from first sighting to baseline resolution).

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

### Deterministic scanner ingestion

For controller-enforced web/API, cryptography, and network wrappers, replay metadata, coverage semantics, and offline fixtures, see [Deterministic security tool packs](./security-tool-packs.md).

Agent findings can be complemented with results from deterministic tools (semgrep, gosec, trivy, gitleaks, ...) that the scan agent runs in the workspace. The agent adapts each tool result into the canonical **scanner record** contract and submits batches with the `ingest_scanner_results` tool — at most 500 records and 4 MiB of input per call. Every record in a batch is validated against the contract; if any record is invalid the whole batch is rejected with per-record errors (`records[3]: rule_id is required; ...`) and nothing is ingested.

```json
{
  "records": [
    {
      "tool": "gosec",
      "tool_version": "2.18.2",
      "rule_id": "G401",
      "rule_name": "Use of weak cryptographic primitive",
      "message": "Use of weak cryptographic primitive md5",
      "severity": "HIGH",
      "file_path": "internal/crypto/hash.go",
      "start_line": 42,
      "end_line": 44,
      "symbol": "hashPassword",
      "cwe": "CWE-327",
      "references": ["https://cwe.mitre.org/data/definitions/327.html"],
      "raw_evidence": "sum := md5.Sum(password)",
      "extra": {"confidence": "HIGH"}
    }
  ]
}
```

`tool`, `rule_id`, `message`, `severity`, and `file_path` are required. Tool severities are mapped onto the platform scale (`ERROR` → high, `WARNING`/`moderate` → medium, `note`/`UNKNOWN` → info, `CRITICAL`/`HIGH`/`MEDIUM`/`LOW` as-is); a severity that cannot be mapped rejects the record. The category is taken from an explicit `category` when given, otherwise derived from the CWE, falling back to `other`. Accepted records go through the same normalization and secret-redaction pipeline as agent findings, and the original scanner record is preserved verbatim (minus redacted secrets) in the finding's raw payload.

**Fingerprints.** A scanner finding's fingerprint is derived from `(tool, rule id, repository, normalized path, symbol-or-line anchor)` — never from the message text — so re-running the same tool converges onto the same finding row (occurrences increment, triage status persists) even across tool upgrades that reword messages. This derivation is deliberately distinct from agent fingerprints (which hash category, location, and title tokens): an agent finding and a scanner finding can never collide on identity, so combining the two sources is always an explicit, audited correlation rather than an accidental merge.

**Correlation, not merging.** When an agent finding and a scanner finding describe the same issue — same file, line ranges overlapping or starting within 5 lines, and either a shared CWE or a matching category — the platform records a correlation on **both** rows (`correlated` audit events, cross-referenced fingerprints). Neither side is deleted or rewritten to look like the other: the agent finding keeps its confidence semantics and narrative, the scanner finding keeps its tool/version/rule identity. Report-time dedupe likewise never merges across the two source kinds. Correlations appear in finding lists, in the summary (`source_agent` / `source_scanner` / `correlated` counts), in the Markdown report ("Correlated with: ..."), and in SARIF.

**Provenance guarantees.** Every finding carries a `source_kind` (`agent` or `scanner`); scanner findings additionally carry the tool name, tool version, and rule id, stamped at ingestion and impossible for the model to forge through `report_security_finding`. Reports attribute each finding to its source: the Markdown report labels findings `agent <run>` or `scanner <tool> <version>, rule <id>`, and the SARIF output emits one run per source — agent findings under the `gratefulagents-security-scan` driver and each scanner's findings under that tool's own driver name, version, and real rule ids, so gratefulagents never claims another tool's rules as its own.

**Neither source is authoritative.** Deterministic tools attest that a rule matched (scanner findings are stored with `firm` confidence), not that the issue is exploitable; agents hypothesize, validate, and explain but can miss or over-report. Use the agent for what each source cannot do alone: prioritize correlated findings first (two independent signals), spend bounded validation effort confirming or disproving scanner matches with `update_security_finding`, and treat agent-only and scanner-only findings as complementary leads rather than letting either side suppress the other.

## Reports, status, and the dashboard

When the scan submits its report, two artifacts are saved on the scan's agent run:

- **`security_report`** — a Markdown report with the executive summary and ranked findings.
- **`security_sarif`** — a SARIF 2.1.0 file suitable for importing into code-scanning tools; each result carries the finding fingerprint for cross-referencing.

In the dashboard, **Security** in the sidebar opens an overview of active and recent scans, open critical/high finding counts, and any scan configurations that are failing, blocked, or suspended, with shortcuts to the full run history and to scan configurations. Each scan run links to a detail page where you can filter findings by severity, status, category, and text search, change a finding's status inline — for example, marking a validated non-issue as `false_positive` or a real one as `confirmed` — download the Markdown report and SARIF artifact, and jump to the underlying agent run.

While a scan is running, the detail page also shows the live state of the run behind it: the workflow's sub-agent graph (pending, running, completed, failed), run phase, retries, model, runtime, token/cost usage, and the most recent error. Collaborators can stop an active scan run from this panel. Stopping cancels every running child AgentRun, marks the deterministic execution **Cancelled** (running tasks become **Failed** and unstarted tasks **Skipped**), settles the scan-run record as `cancelled`, and prevents further work from being scheduled. Stop is rejected when nothing is running. Findings recorded before the stop remain available. A cancelled execution cannot be resumed; start a new run instead. **Resume** applies only to a **Failed** deterministic execution.

### Scan configurations

On **Scan configurations**, select individual rows or select all visible rows, then use **Run now**, **Stop**, **Suspend**, **Resume**, or **Delete**. The bulk toolbar reports the number changed and lists each configuration that failed, so you can retry those items. Deleting a configuration also removes its recorded scan runs and findings, so export them first if you need them.

Select **Import scan targets** to import security-program targets in bulk. Select individual targets or **Select all**; targets that already have a configuration are unavailable and skipped. Import creates every selected configuration but starts no scans. Imported configurations use saved credentials and default model settings, a manual-only schedule, workspace-write access, unrestricted network egress, minimum severity `high`, parallelism `4`, and deduplication enabled. Select **Configure scan** for one target when you want to review its prefilled configuration before creating it.

**Run now** starts an immediate run of a configuration without editing its spec (`concurrencyPolicy: Forbid` still applies: the request is skipped with a `ConcurrencyBlocked` status while a previous run is active), and **Duplicate** opens the scan form pre-filled from an existing configuration so you can review the copied settings and create it under a new name.

On the cluster side, `kubectl get securityscans` shows the repository, schedule, last scan time, and critical/high/total finding counts. The resource status also records the last run name, next scheduled time, cumulative runs created, scan-scoped finding counts, and a `Ready` condition. With `failOnSeverity` set, `Ready` turns `False` with reason `FindingsExceedThreshold` while open scan findings at or above that severity exist — useful for alerting on scan results.

## Scheduling behavior

- **One-shot** (no `schedule`): the scan runs when created and again whenever the spec generation changes.
- **Scheduled**: standard five-field cron expressions and descriptors such as `@daily` are supported, evaluated in `timeZone` (default UTC). The default `concurrencyPolicy` is `Forbid`: a due tick is skipped while a previous scan run is still active. Set `Allow` to permit overlapping runs. When a scheduled run finishes, the scan phase becomes `Completed` until the next run starts.
- **Suspend**: set `spec.suspend: true` to pause new scan runs without deleting the resource; status stays readable.

A malformed schedule sets `Ready=False` with reason `InvalidSchedule`. A schedule that parses but cannot produce a next time sets `Ready=False` with reason `ScheduleError` instead of repeatedly reconciling it.

Skipped ticks are not backfilled, matching [Cron schedule](./cron.md) semantics.

**Deletion.** Deleting a SecurityScan also removes its recorded scan runs and findings, so export them first if you need them. The run artifacts (Markdown report and SARIF) live with their agent runs and follow the run's own lifecycle.

## Retention and budgets

A `SecurityPolicyPack` referenced by `spec.policyPackRef` can govern how long scan data is kept and how much a scan run may consume.

### Retention

`spec.retention` on the policy pack sets per-class day counts. `0` (or omitting a class) keeps that class forever — retention is entirely opt-in, and nothing is purged by default.

```yaml
apiVersion: triggers.gratefulagents.dev/v1alpha1
kind: SecurityPolicyPack
metadata:
  name: org-policy
spec:
  retention:
    scanDays: 180        # completed scan-run records and per-run observations
    findingDays: 365     # finding rows (deleted with their audit events)
    reportDays: 90       # Markdown/SARIF report artifacts
    evidenceDays: 30     # evidence snippets — redacted in place
    pocDays: 14          # PoC / attack-vector narratives — redacted in place
    auditEventDays: 730  # finding audit-trail events
```

Every count must be between 0 and 3650 days (10 years).

**How the sweep works.** The SecurityScan controller runs the purge as a bounded, resumable background sweep: at most one small batch per reconcile (each class purged by its own namespace-scoped, deterministically ordered, LIMIT-bounded statement), requeuing promptly while a batch reports more work and hourly otherwise. The outcome is observable on the scan: `status.retention` records the last sweep time, cumulative per-class counters, the more-work flag, and the last error, and a `RetentionSweep` event carries each batch's counts. The sweep never runs in the deletion path, so retention work can never slow or wedge the scan-deletion finalizer and its bounded (15-minute) cleanup guarantee.

**What deletion vs. redaction means.** Scan-run records, finding rows, report artifacts, and audit events past their windows are **deleted**. Evidence and PoC content are **redacted in place** instead: the finding row keeps its identity, severity, triage status, and full audit history, only the code snippets (`evidence`) and the exploit narrative (`attack_vector`) are removed, and an `evidence_purged` / `poc_purged` audit event records the redaction. A scan-run record is only deleted once no finding is attributed to it anymore, so finding identity is never cascade-deleted by scan retention.

**Privacy implications of evidence retention.** Evidence snippets are verbatim copies of your source code — including any secrets a finding cites — and PoC narratives describe how to exploit the issue. They are the most sensitive data the scanner stores. If your compliance posture limits how long source excerpts or exploit instructions may live outside the repository, set `evidenceDays`/`pocDays` shorter than `findingDays`: the finding remains actionable (title, location, severity, remediation, audit trail) while the sensitive payload ages out.

**Migration and rollback.** Retention ships with store migration `047_security_retention`: it only adds purge-supporting indexes and extends the artifact-kind constraint to cover the `security_report`/`security_sarif` artifact kinds. It is applied automatically on startup, additive, and safe on populated databases. Rolling back (`047_security_retention.down.sql`) drops the indexes and restores the previous artifact-kind constraint; no data is modified in either direction. Purged data itself is not recoverable by rollback — take database backups before enabling aggressive retention, and note that redactions are recorded in the audit trail, so you can always tell what was removed by policy.

### Budgets

`spec.budgets` caps what one scan run may consume. It exists on both the policy pack (defaults for every referencing scan) and the scan (`SecurityScan.spec.budgets`); precedence matches every other pack field — pack default < scan — unless the pack lists `budgets` in `enforced`, in which case a scan may tighten but never raise a limit the pack sets.

```yaml
spec:
  budgets:
    maxModelJobs: 16       # sub-agent runs the scan run may spawn
    maxCostUSD: "5"        # decimal USD ceiling on LLM spend
    maxTokens: 500000      # total tokens (input + output)
    maxRuntime: 2h         # wall-clock cap on the scan run
    maxValidationJobs: 8   # post-script (validation/PoC) sub-agent runs
  enforced: ["budgets"]
```

Enforcement is entirely platform-side and model output can never relax it:

- Every limit is computed from the CRD spec merged with the policy pack **before** the run prompt is built; a scan that raises an enforced limit is rejected with `Ready=False` reason `PolicyViolation` and no run is created.
- What the run can self-limit is written into the created AgentRun's limits: `maxRuntime` (the smallest of `budgets.maxRuntime`, `spec.maxRuntime`, and `defaults.timeout` wins) and `maxCostUSD`.
- Everything else is monitored by the controller on each reconcile from platform-observed data: cost and tokens from the run's usage metrics and model/validation jobs from the run's child status.
- When a hard limit is exceeded, the controller cancels the run gracefully (the same cancel-requested mechanism the dashboard stop button uses) and sets `Ready=False` with reason `BudgetExceeded`, a message naming the exceeded limit, and a warning event. **Completed work is preserved**: findings already persisted, reports, and the scan's status survive; nothing is deleted.
- The effective budgets and any already-exceeded state are published on `status.budget` (and through the dashboard API), so an operator can inspect the current budget state before starting the next run.

## Operational guidance

**Cost and models.** A scan is expensive relative to a normal run: every workflow task is its own sub-agent that reads the repository, so cost scales with the number of tasks, repository size, and model choice. The default workflow launches twelve sub-agents. To control spend: narrow `scope.includePaths` and `scope.languages`, replace the default workflow with fewer targeted tasks, use a cheaper `defaults.model` and reserve a stronger per-task `model` override for the triage task, and set `maxRuntime` as a hard stop.

**Parallelism.** `parallelism` (default 4) trades wall-clock time against concurrent load: each in-flight task is a live sub-agent consuming provider rate limits and sandbox resources. Raise it toward 16 for wide, independent workflows when your provider limits allow; lower it to 1–2 on constrained clusters or strict rate limits.

**Triage discipline.** Treat a fresh scan as a candidate list, not a report you forward. Validate each finding against the cited evidence, mark non-issues `false_positive` (the audit trail keeps them from being re-litigated on the next scan — reported duplicates merge into the existing finding instead of reopening it), mark real issues `confirmed`, and move them to `fixed` or `accepted_risk` once resolved. Changing a false positive, or any other finding, away from `open` updates the scan's open counts and clears its contribution to `failOnSeverity`. Use `postScripts` with `runOn: high-and-above` to make the scan itself re-verify its most important findings before they reach you, and ranker `exclude:` directives to silence categories that are consistently noise in your codebase.
