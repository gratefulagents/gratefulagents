import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";

import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { FlowField } from "@/components/create-flow/create-flow";
import { client } from "@/lib/client";
import {
  CreateSecurityPolicyPackRequestSchema,
  SecurityPolicyPackResourceSchema,
  SecurityPolicyPackRetentionConfigSchema,
  SecurityPolicySuppressionConfigSchema,
  SecurityScanBudgetsConfigSchema,
  SecurityScanDedupeConfigSchema,
  SecuritySuppressionMatcherConfigSchema,
  UpdateSecurityPolicyPackRequestSchema,
  type SecurityPolicyPackResource,
  type SecurityPolicyPackRetentionConfig,
  type SecurityScanBudgetsConfig,
} from "@/rpc/platform/service_pb";

const selectClass = "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

export const SEVERITY_OPTIONS = ["critical", "high", "medium", "low", "info"] as const;

/** Mirrors SecurityPolicyPackEnforceableFields on the server. */
export const ENFORCEABLE_FIELDS = [
  { field: "minSeverity", label: "Minimum severity" },
  { field: "failOnSeverity", label: "Fail-on severity" },
  { field: "dedupe", label: "Dedupe" },
  { field: "requiredCategories", label: "Required categories" },
  { field: "allowedRuntimeProfiles", label: "Allowed runtime profiles" },
  { field: "budgets", label: "Budgets" },
] as const;

/** Retention day inputs, one per persisted data class. */
const RETENTION_CLASSES = [
  { key: "scanDays", label: "Scan runs", hint: "Completed scan run records and observation rows." },
  { key: "findingDays", label: "Findings", hint: "Finding rows, deleted with their audit events." },
  { key: "reportDays", label: "Reports", hint: "Report artifacts (Markdown and SARIF)." },
  { key: "evidenceDays", label: "Evidence", hint: "Evidence snippets, redacted in place." },
  { key: "pocDays", label: "PoC", hint: "PoC / attack-vector narratives, redacted in place." },
  { key: "auditEventDays", label: "Audit events", hint: "Finding audit-trail events." },
] as const;

export const RETENTION_MAX_DAYS = 3650;

export type BudgetDraft = {
  maxModelJobs: string;
  maxCostUsd: string;
  maxTokens: string;
  maxRuntime: string;
  maxFindings: string;
  maxValidationJobs: string;
};

export type SuppressionDraft = {
  name: string;
  reason: string;
  owner: string;
  category: string;
  cwe: string;
  pathGlob: string;
  fingerprint: string;
  /** yyyy-mm-dd; empty = no expiry */
  expiresAt: string;
};

type RetentionDraft = Record<(typeof RETENTION_CLASSES)[number]["key"], string>;

export type PolicyPackDraft = {
  name: string;
  description: string;
  minSeverity: string;
  failOnSeverity: string;
  requiredCategories: string;
  allowedRuntimeProfiles: string;
  defaultRankerRefs: string;
  defaultPostScriptRefs: string;
  /** "" = pack does not set dedupe; "on"/"off" set it. */
  dedupeMode: "" | "on" | "off";
  dedupeThreshold: string;
  enforced: string[];
  retention: RetentionDraft;
  budgets: BudgetDraft;
  suppressions: SuppressionDraft[];
};

export type PackFieldError = { field: string; message: string };

const DNS1123_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const COST_PATTERN = /^([0-9]+(\.[0-9]+)?)?$/;
// Light client-side check for Go duration strings like "2h" or "90m".
const DURATION_PATTERN = /^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$/;

function splitList(value: string): string[] {
  return value
    .split(/[,\n]/)
    .map((v) => v.trim())
    .filter(Boolean);
}

function emptyBudgets(): BudgetDraft {
  return { maxModelJobs: "", maxCostUsd: "", maxTokens: "", maxRuntime: "", maxFindings: "", maxValidationJobs: "" };
}

export function budgetDraftIsZero(budgets: BudgetDraft): boolean {
  return Object.values(budgets).every((v) => !v.trim() || v.trim() === "0");
}

/** budgetsFromDraft builds the budgets proto, or undefined when all-empty. */
export function budgetsFromDraft(budgets: BudgetDraft): SecurityScanBudgetsConfig | undefined {
  if (budgetDraftIsZero(budgets)) return undefined;
  return create(SecurityScanBudgetsConfigSchema, {
    maxModelJobs: budgets.maxModelJobs.trim() ? Number(budgets.maxModelJobs) : 0,
    maxCostUsd: budgets.maxCostUsd.trim(),
    maxTokens: budgets.maxTokens.trim() ? BigInt(budgets.maxTokens.trim()) : 0n,
    maxRuntime: budgets.maxRuntime.trim(),
    maxFindings: budgets.maxFindings.trim() ? Number(budgets.maxFindings) : 0,
    maxValidationJobs: budgets.maxValidationJobs.trim() ? Number(budgets.maxValidationJobs) : 0,
  });
}

function emptyRetention(): RetentionDraft {
  return { scanDays: "", findingDays: "", reportDays: "", evidenceDays: "", pocDays: "", auditEventDays: "" };
}

export function emptyPolicyPackDraft(): PolicyPackDraft {
  return {
    name: "",
    description: "",
    minSeverity: "",
    failOnSeverity: "",
    requiredCategories: "",
    allowedRuntimeProfiles: "",
    defaultRankerRefs: "",
    defaultPostScriptRefs: "",
    dedupeMode: "",
    dedupeThreshold: "",
    enforced: [],
    retention: emptyRetention(),
    budgets: emptyBudgets(),
    suppressions: [],
  };
}

export function budgetsToDraft(budgets?: SecurityScanBudgetsConfig): BudgetDraft {
  if (!budgets) return emptyBudgets();
  return {
    maxModelJobs: budgets.maxModelJobs ? String(budgets.maxModelJobs) : "",
    maxCostUsd: budgets.maxCostUsd,
    maxTokens: budgets.maxTokens ? String(budgets.maxTokens) : "",
    maxRuntime: budgets.maxRuntime,
    maxFindings: budgets.maxFindings ? String(budgets.maxFindings) : "",
    maxValidationJobs: budgets.maxValidationJobs ? String(budgets.maxValidationJobs) : "",
  };
}

function retentionToDraft(retention?: SecurityPolicyPackRetentionConfig): RetentionDraft {
  if (!retention) return emptyRetention();
  const day = (d: number) => (d ? String(d) : "");
  return {
    scanDays: day(retention.scanDays),
    findingDays: day(retention.findingDays),
    reportDays: day(retention.reportDays),
    evidenceDays: day(retention.evidenceDays),
    pocDays: day(retention.pocDays),
    auditEventDays: day(retention.auditEventDays),
  };
}

export function policyPackToDraft(pack: SecurityPolicyPackResource): PolicyPackDraft {
  return {
    name: pack.name,
    description: pack.description,
    minSeverity: pack.minSeverity,
    failOnSeverity: pack.failOnSeverity,
    requiredCategories: pack.requiredCategories.join(", "),
    allowedRuntimeProfiles: pack.allowedRuntimeProfiles.join(", "),
    defaultRankerRefs: pack.defaultRankerRefs.join(", "),
    defaultPostScriptRefs: pack.defaultPostScriptRefs.join(", "),
    dedupeMode: pack.dedupe ? (pack.dedupe.enabled ? "on" : "off") : "",
    dedupeThreshold: pack.dedupe?.similarityThresholdPermille
      ? String(pack.dedupe.similarityThresholdPermille)
      : "",
    enforced: [...pack.enforced],
    retention: retentionToDraft(pack.retention),
    budgets: budgetsToDraft(pack.budgets),
    suppressions: pack.suppressions.map((rule) => ({
      name: rule.name,
      reason: rule.reason,
      owner: rule.owner,
      category: rule.matcher?.category ?? "",
      cwe: rule.matcher?.cwe ?? "",
      pathGlob: rule.matcher?.pathGlob ?? "",
      fingerprint: rule.matcher?.fingerprint ?? "",
      expiresAt: rule.expiresAt
        ? timestampDate(rule.expiresAt).toISOString().slice(0, 10)
        : "",
    })),
  };
}

function nonNegativeInt(value: string): boolean {
  if (!value.trim()) return true;
  const n = Number(value);
  return Number.isInteger(n) && n >= 0;
}

/** validateBudgetDraft mirrors ValidateSecurityScanBudgets on the server. */
export function validateBudgetDraft(prefix: string, budgets: BudgetDraft): PackFieldError[] {
  const errs: PackFieldError[] = [];
  if (!nonNegativeInt(budgets.maxModelJobs)) {
    errs.push({ field: `${prefix}.maxModelJobs`, message: "must be a non-negative whole number (0 = unlimited)" });
  }
  if (!COST_PATTERN.test(budgets.maxCostUsd.trim())) {
    errs.push({ field: `${prefix}.maxCostUSD`, message: 'must be a plain decimal like "5" or "2.50"' });
  }
  if (!nonNegativeInt(budgets.maxTokens)) {
    errs.push({ field: `${prefix}.maxTokens`, message: "must be a non-negative whole number (0 = unlimited)" });
  }
  if (budgets.maxRuntime.trim() && !DURATION_PATTERN.test(budgets.maxRuntime.trim())) {
    errs.push({ field: `${prefix}.maxRuntime`, message: 'must be a duration like "2h" or "90m"' });
  }
  if (!nonNegativeInt(budgets.maxFindings)) {
    errs.push({ field: `${prefix}.maxFindings`, message: "must be a non-negative whole number (0 = unlimited)" });
  }
  if (!nonNegativeInt(budgets.maxValidationJobs)) {
    errs.push({ field: `${prefix}.maxValidationJobs`, message: "must be a non-negative whole number (0 = unlimited)" });
  }
  return errs;
}

/**
 * validatePolicyPackDraft mirrors ValidateSecurityPolicyPackSpec on the
 * server so most mistakes are caught before the request; the server re-runs
 * the same rules and its errors are surfaced verbatim in the dialog.
 */
export function validatePolicyPackDraft(draft: PolicyPackDraft): PackFieldError[] {
  const errs: PackFieldError[] = [];
  const add = (field: string, message: string) => errs.push({ field, message });

  if (!draft.name.trim()) {
    add("name", "name is required");
  }
  if (draft.dedupeMode !== "" && draft.dedupeThreshold.trim()) {
    const n = Number(draft.dedupeThreshold);
    if (!Number.isInteger(n) || n < 0 || n > 1000) {
      add("dedupe.similarityThresholdPermille", "threshold out of range (want 0-1000)");
    }
  }
  for (const cls of RETENTION_CLASSES) {
    const value = draft.retention[cls.key].trim();
    if (!value) continue;
    const n = Number(value);
    if (!Number.isInteger(n) || n < 0 || n > RETENTION_MAX_DAYS) {
      add(`retention.${cls.key}`, `days out of range (want 0 for keep-forever, or 1-${RETENTION_MAX_DAYS})`);
    }
  }
  errs.push(...validateBudgetDraft("budgets", draft.budgets));

  for (const field of draft.enforced) {
    switch (field) {
      case "minSeverity":
        if (!draft.minSeverity) add("enforced", "enforcing minSeverity requires minSeverity to be set");
        break;
      case "failOnSeverity":
        if (!draft.failOnSeverity) add("enforced", "enforcing failOnSeverity requires failOnSeverity to be set");
        break;
      case "requiredCategories":
        if (splitList(draft.requiredCategories).length === 0) {
          add("enforced", "enforcing requiredCategories requires a non-empty category list");
        }
        break;
      case "allowedRuntimeProfiles":
        if (splitList(draft.allowedRuntimeProfiles).length === 0) {
          add("enforced", "enforcing allowedRuntimeProfiles requires a non-empty profile list");
        }
        break;
      case "budgets":
        if (budgetDraftIsZero(draft.budgets)) {
          add("enforced", "enforcing budgets requires at least one budget limit to be set");
        }
        break;
    }
  }

  const seenRules = new Set<string>();
  draft.suppressions.forEach((rule, i) => {
    const path = `suppressions[${i}]`;
    const name = rule.name.trim();
    if (!DNS1123_LABEL.test(name) || name.length > 63) {
      add(`${path}.name`, "rule name must be a lowercase DNS-1123 label (letters, digits, dashes)");
    } else if (seenRules.has(name)) {
      add(`${path}.name`, `duplicate rule name "${name}": rule names must be unique`);
    }
    seenRules.add(name);
    if (!rule.reason.trim()) add(`${path}.reason`, "a reason is required");
    if (!rule.owner.trim()) add(`${path}.owner`, "an owner is required");
    if (!rule.category.trim() && !rule.cwe.trim() && !rule.pathGlob.trim() && !rule.fingerprint.trim()) {
      add(`${path}.matcher`, "at least one matcher field is required (category, CWE, path glob, or fingerprint)");
    }
    if (rule.expiresAt && Number.isNaN(new Date(`${rule.expiresAt}T00:00:00Z`).getTime())) {
      add(`${path}.expiresAt`, "invalid expiry date");
    }
  });

  return errs;
}

export function buildPolicyPackResource(draft: PolicyPackDraft): SecurityPolicyPackResource {
  const budgets = budgetsFromDraft(draft.budgets);
  const retentionDays = (key: (typeof RETENTION_CLASSES)[number]["key"]) =>
    draft.retention[key].trim() ? Number(draft.retention[key]) : 0;
  const retentionSet = RETENTION_CLASSES.some((cls) => retentionDays(cls.key) > 0);
  return create(SecurityPolicyPackResourceSchema, {
    name: draft.name.trim(),
    description: draft.description.trim(),
    minSeverity: draft.minSeverity,
    failOnSeverity: draft.failOnSeverity,
    requiredCategories: splitList(draft.requiredCategories),
    allowedRuntimeProfiles: splitList(draft.allowedRuntimeProfiles),
    defaultRankerRefs: splitList(draft.defaultRankerRefs),
    defaultPostScriptRefs: splitList(draft.defaultPostScriptRefs),
    dedupe:
      draft.dedupeMode === ""
        ? undefined
        : create(SecurityScanDedupeConfigSchema, {
            enabled: draft.dedupeMode === "on",
            similarityThresholdPermille: draft.dedupeThreshold.trim()
              ? Number(draft.dedupeThreshold)
              : 0,
          }),
    enforced: [...draft.enforced],
    retention: retentionSet
      ? create(SecurityPolicyPackRetentionConfigSchema, {
          scanDays: retentionDays("scanDays"),
          findingDays: retentionDays("findingDays"),
          reportDays: retentionDays("reportDays"),
          evidenceDays: retentionDays("evidenceDays"),
          pocDays: retentionDays("pocDays"),
          auditEventDays: retentionDays("auditEventDays"),
        })
      : undefined,
    budgets,
    suppressions: draft.suppressions.map((rule) =>
      create(SecurityPolicySuppressionConfigSchema, {
        name: rule.name.trim(),
        reason: rule.reason.trim(),
        owner: rule.owner.trim(),
        matcher: create(SecuritySuppressionMatcherConfigSchema, {
          category: rule.category.trim(),
          cwe: rule.cwe.trim(),
          pathGlob: rule.pathGlob.trim(),
          fingerprint: rule.fingerprint.trim(),
        }),
        expiresAt: rule.expiresAt
          ? timestampFromDate(new Date(`${rule.expiresAt}T00:00:00Z`))
          : undefined,
      }),
    ),
  });
}

/** packBudgetSummary renders a budgets block as one plain-text line. */
export function packBudgetSummary(budgets?: SecurityScanBudgetsConfig): string {
  if (!budgets) return "";
  const parts: string[] = [];
  if (budgets.maxCostUsd) parts.push(`$${budgets.maxCostUsd}`);
  if (budgets.maxTokens) parts.push(`${budgets.maxTokens} tokens`);
  if (budgets.maxModelJobs) parts.push(`${budgets.maxModelJobs} jobs`);
  if (budgets.maxValidationJobs) parts.push(`${budgets.maxValidationJobs} validation jobs`);
  if (budgets.maxRuntime) parts.push(budgets.maxRuntime);
  if (budgets.maxFindings) parts.push(`${budgets.maxFindings} findings`);
  return parts.join(" · ");
}

/** packRetentionSummary renders a retention block as one plain-text line. */
export function packRetentionSummary(retention?: SecurityPolicyPackRetentionConfig): string {
  if (!retention) return "";
  const parts: string[] = [];
  const push = (label: string, days: number) => {
    if (days > 0) parts.push(`${label} ${days}d`);
  };
  push("scans", retention.scanDays);
  push("findings", retention.findingDays);
  push("reports", retention.reportDays);
  push("evidence", retention.evidenceDays);
  push("PoC", retention.pocDays);
  push("audit", retention.auditEventDays);
  return parts.join(" · ");
}

function emptySuppression(): SuppressionDraft {
  return { name: "", reason: "", owner: "", category: "", cwe: "", pathGlob: "", fingerprint: "", expiresAt: "" };
}

/**
 * Create/edit/duplicate dialog for SecurityPolicyPack resources. Client-side
 * validation mirrors ValidateSecurityPolicyPackSpec; the server re-validates
 * and its structured errors are shown verbatim.
 */
export function PolicyPackEditorDialog({
  source,
  mode,
  trigger,
  onSaved,
}: {
  source?: SecurityPolicyPackResource;
  mode: "create" | "edit" | "duplicate";
  trigger: React.ReactElement;
  onSaved: () => void;
}) {
  const isEdit = mode === "edit";

  function initialDraft(): PolicyPackDraft {
    if (!source) return emptyPolicyPackDraft();
    const draft = policyPackToDraft(source);
    if (mode === "duplicate") draft.name = "";
    return draft;
  }

  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<PolicyPackDraft>(initialDraft);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<PackFieldError[]>([]);

  function update<K extends keyof PolicyPackDraft>(field: K, value: PolicyPackDraft[K]) {
    setDraft((prev) => ({ ...prev, [field]: value }));
  }

  function updateSuppression(index: number, patch: Partial<SuppressionDraft>) {
    setDraft((prev) => ({
      ...prev,
      suppressions: prev.suppressions.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)),
    }));
  }

  function reset() {
    setDraft(initialDraft());
    setError(null);
    setFieldErrors([]);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    const errs = validatePolicyPackDraft(draft);
    setFieldErrors(errs);
    if (errs.length > 0) return;
    setSubmitting(true);
    try {
      const resource = buildPolicyPackResource(draft);
      if (isEdit) {
        await client.updateSecurityPolicyPack(
          create(UpdateSecurityPolicyPackRequestSchema, { policyPack: resource }),
        );
      } else {
        await client.createSecurityPolicyPack(
          create(CreateSecurityPolicyPackRequestSchema, { policyPack: resource }),
        );
      }
      setOpen(false);
      reset();
      onSaved();
    } catch (err) {
      // Server validation errors arrive as InvalidArgument messages listing
      // the offending fields; show them verbatim.
      setError(err instanceof Error ? err.message : "Failed to save policy pack");
    } finally {
      setSubmitting(false);
    }
  }

  const fieldError = (field: string) => fieldErrors.find((e) => e.field.startsWith(field))?.message;

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { setOpen(nextOpen); if (!nextOpen) reset(); }}>
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-3xl flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl max-h-[92vh]" showCloseButton>
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <DialogTitle className="text-base">
              {isEdit
                ? `Edit policy pack ${source?.name}`
                : mode === "duplicate"
                  ? `Duplicate policy pack ${source?.name}`
                  : "New policy pack"}
            </DialogTitle>
            <DialogDescription>
              A policy pack supplies scan defaults, enforced floors referencing scans may not
              relax, governed finding suppressions, data retention, and budgets.
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <div className="grid gap-3 sm:grid-cols-2">
              <FlowField id="pp-name" label="Name" required>
                <Input
                  id="pp-name"
                  value={draft.name}
                  onChange={(event) => update("name", event.target.value)}
                  disabled={isEdit}
                  placeholder="prod-policy"
                  className="font-mono"
                />
              </FlowField>
              <FlowField id="pp-description" label="Description">
                <Input
                  id="pp-description"
                  value={draft.description}
                  onChange={(event) => update("description", event.target.value)}
                  placeholder="What this pack governs."
                />
              </FlowField>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <FlowField
                id="pp-min-severity"
                label="Minimum severity"
                hint="Findings below this severity are dropped by referencing scans."
              >
                <select
                  id="pp-min-severity"
                  className={selectClass}
                  value={draft.minSeverity}
                  onChange={(event) => update("minSeverity", event.target.value)}
                >
                  <option value="">not set by pack</option>
                  {SEVERITY_OPTIONS.map((s) => (
                    <option key={s} value={s}>{s}</option>
                  ))}
                </select>
              </FlowField>
              <FlowField
                id="pp-fail-on-severity"
                label="Fail on severity"
                hint="Referencing scans go not-ready when findings at or above this severity exist."
              >
                <select
                  id="pp-fail-on-severity"
                  className={selectClass}
                  value={draft.failOnSeverity}
                  onChange={(event) => update("failOnSeverity", event.target.value)}
                >
                  <option value="">not set by pack</option>
                  {SEVERITY_OPTIONS.map((s) => (
                    <option key={s} value={s}>{s}</option>
                  ))}
                </select>
              </FlowField>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <FlowField
                id="pp-required-categories"
                label="Required categories"
                hint="Comma-separated; the effective scan workflow must cover each one when enforced."
              >
                <Input
                  id="pp-required-categories"
                  value={draft.requiredCategories}
                  onChange={(event) => update("requiredCategories", event.target.value)}
                  placeholder="injection, auth"
                />
              </FlowField>
              <FlowField
                id="pp-allowed-profiles"
                label="Allowed runtime profiles"
                hint="Comma-separated RuntimeProfile names scans must use when enforced."
              >
                <Input
                  id="pp-allowed-profiles"
                  value={draft.allowedRuntimeProfiles}
                  onChange={(event) => update("allowedRuntimeProfiles", event.target.value)}
                  placeholder="locked-down"
                  className="font-mono"
                />
              </FlowField>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <FlowField
                id="pp-default-rankers"
                label="Default ranker refs"
                hint="Comma-separated SecurityRanker names appended to referencing scans."
              >
                <Input
                  id="pp-default-rankers"
                  value={draft.defaultRankerRefs}
                  onChange={(event) => update("defaultRankerRefs", event.target.value)}
                  placeholder="payments-ranker"
                  className="font-mono"
                />
              </FlowField>
              <FlowField
                id="pp-default-post-scripts"
                label="Default post-script refs"
                hint="Comma-separated SecurityPostScript names appended to referencing scans."
              >
                <Input
                  id="pp-default-post-scripts"
                  value={draft.defaultPostScriptRefs}
                  onChange={(event) => update("defaultPostScriptRefs", event.target.value)}
                  placeholder="write-poc"
                  className="font-mono"
                />
              </FlowField>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <FlowField id="pp-dedupe" label="Dedupe" hint="Duplicate-finding suppression for referencing scans.">
                <select
                  id="pp-dedupe"
                  className={selectClass}
                  value={draft.dedupeMode}
                  onChange={(event) => update("dedupeMode", event.target.value as PolicyPackDraft["dedupeMode"])}
                >
                  <option value="">not set by pack</option>
                  <option value="on">on</option>
                  <option value="off">off</option>
                </select>
              </FlowField>
              {draft.dedupeMode !== "" && (
                <FlowField
                  id="pp-dedupe-threshold"
                  label="Similarity threshold (permille)"
                  hint="0-1000; empty = server default (820)."
                >
                  <Input
                    id="pp-dedupe-threshold"
                    type="number"
                    min={0}
                    max={1000}
                    value={draft.dedupeThreshold}
                    onChange={(event) => update("dedupeThreshold", event.target.value)}
                    placeholder="820"
                  />
                </FlowField>
              )}
            </div>
            {fieldError("dedupe") && <p className="text-xs text-destructive">{fieldError("dedupe")}</p>}

            <fieldset className="space-y-2">
              <legend className="text-[13px] font-medium">Enforced fields</legend>
              <p className="text-xs text-muted-foreground">
                Scans may not relax these: a referencing scan that weakens an enforced field is
                rejected by the platform.
              </p>
              <div className="flex flex-wrap gap-2">
                {ENFORCEABLE_FIELDS.map(({ field, label }) => {
                  const checked = draft.enforced.includes(field);
                  return (
                    <label
                      key={field}
                      className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border px-2 py-1 text-xs"
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() =>
                          update(
                            "enforced",
                            checked
                              ? draft.enforced.filter((f) => f !== field)
                              : [...draft.enforced, field],
                          )
                        }
                      />
                      {label}
                    </label>
                  );
                })}
              </div>
              {fieldError("enforced") && (
                <p className="text-xs text-destructive" data-testid="pp-enforced-error">
                  {fieldError("enforced")}
                </p>
              )}
            </fieldset>

            <fieldset className="space-y-2">
              <legend className="text-[13px] font-medium">Retention (days)</legend>
              <p className="text-xs text-muted-foreground">
                How long each class of persisted security data is kept. 0 or empty = keep
                forever. Evidence and PoC content is redacted in place when it expires — keep
                these short if scans may capture sensitive data.
              </p>
              <div className="grid gap-3 sm:grid-cols-3">
                {RETENTION_CLASSES.map((cls) => (
                  <FlowField key={cls.key} id={`pp-retention-${cls.key}`} label={cls.label} hint={cls.hint}>
                    <Input
                      id={`pp-retention-${cls.key}`}
                      type="number"
                      min={0}
                      max={RETENTION_MAX_DAYS}
                      value={draft.retention[cls.key]}
                      onChange={(event) =>
                        update("retention", { ...draft.retention, [cls.key]: event.target.value })
                      }
                      placeholder="0"
                    />
                  </FlowField>
                ))}
              </div>
              {fieldError("retention") && <p className="text-xs text-destructive">{fieldError("retention")}</p>}
            </fieldset>

            <fieldset className="space-y-2">
              <legend className="text-[13px] font-medium">Budgets</legend>
              <p className="text-xs text-muted-foreground">
                Default per-run caps for referencing scans, enforced entirely platform-side.
                Empty or 0 = unlimited.
              </p>
              <BudgetFields
                idPrefix="pp-budget"
                value={draft.budgets}
                onChange={(budgets) => update("budgets", budgets)}
              />
              {fieldError("budgets") && <p className="text-xs text-destructive">{fieldError("budgets")}</p>}
            </fieldset>

            <fieldset className="space-y-2">
              <legend className="text-[13px] font-medium">Governed suppressions</legend>
              <p className="text-xs text-muted-foreground">
                Matching findings are marked suppressed (never deleted), audited, excluded from
                gating and default listings, and automatically unsuppressed past the expiry.
              </p>
              {draft.suppressions.map((rule, index) => (
                <div key={index} className="space-y-3 rounded-md border p-3" data-testid={`pp-suppression-${index}`}>
                  <div className="grid gap-3 sm:grid-cols-3">
                    <FlowField id={`pp-sup-name-${index}`} label="Rule name" required>
                      <Input
                        id={`pp-sup-name-${index}`}
                        value={rule.name}
                        onChange={(event) => updateSuppression(index, { name: event.target.value })}
                        placeholder="vendored-code"
                        className="font-mono"
                      />
                    </FlowField>
                    <FlowField id={`pp-sup-owner-${index}`} label="Owner" required hint="Who is accountable.">
                      <Input
                        id={`pp-sup-owner-${index}`}
                        value={rule.owner}
                        onChange={(event) => updateSuppression(index, { owner: event.target.value })}
                        placeholder="security-team"
                      />
                    </FlowField>
                    <FlowField
                      id={`pp-sup-expires-${index}`}
                      label="Expires"
                      hint="Optional; findings unsuppress automatically past this date."
                    >
                      <Input
                        id={`pp-sup-expires-${index}`}
                        type="date"
                        value={rule.expiresAt}
                        onChange={(event) => updateSuppression(index, { expiresAt: event.target.value })}
                      />
                    </FlowField>
                  </div>
                  <FlowField id={`pp-sup-reason-${index}`} label="Reason" required hint="Why the findings are suppressed.">
                    <Input
                      id={`pp-sup-reason-${index}`}
                      value={rule.reason}
                      onChange={(event) => updateSuppression(index, { reason: event.target.value })}
                      placeholder="Third-party code, tracked upstream."
                    />
                  </FlowField>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <FlowField id={`pp-sup-category-${index}`} label="Match category" hint="Exact finding category.">
                      <Input
                        id={`pp-sup-category-${index}`}
                        value={rule.category}
                        onChange={(event) => updateSuppression(index, { category: event.target.value })}
                        placeholder="injection"
                      />
                    </FlowField>
                    <FlowField id={`pp-sup-cwe-${index}`} label="Match CWE" hint="Findings whose CWE list contains this id.">
                      <Input
                        id={`pp-sup-cwe-${index}`}
                        value={rule.cwe}
                        onChange={(event) => updateSuppression(index, { cwe: event.target.value })}
                        placeholder="CWE-89"
                        className="font-mono"
                      />
                    </FlowField>
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <FlowField
                      id={`pp-sup-path-${index}`}
                      label="Match path glob"
                      hint="Glob over the finding file path ('*' and '?')."
                    >
                      <Input
                        id={`pp-sup-path-${index}`}
                        value={rule.pathGlob}
                        onChange={(event) => updateSuppression(index, { pathGlob: event.target.value })}
                        placeholder="vendor/**"
                        className="font-mono"
                      />
                    </FlowField>
                    <FlowField id={`pp-sup-fingerprint-${index}`} label="Match fingerprint" hint="Exact finding fingerprint.">
                      <Input
                        id={`pp-sup-fingerprint-${index}`}
                        value={rule.fingerprint}
                        onChange={(event) => updateSuppression(index, { fingerprint: event.target.value })}
                        className="font-mono"
                      />
                    </FlowField>
                  </div>
                  {fieldErrors
                    .filter((e) => e.field.startsWith(`suppressions[${index}]`))
                    .map((e) => (
                      <p key={e.field} className="text-xs text-destructive">
                        {e.message}
                      </p>
                    ))}
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      update("suppressions", draft.suppressions.filter((_, i) => i !== index))
                    }
                  >
                    Remove rule
                  </Button>
                </div>
              ))}
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => update("suppressions", [...draft.suppressions, emptySuppression()])}
              >
                Add suppression rule
              </Button>
            </fieldset>

            {fieldErrors.length > 0 && (
              <ul className="list-disc pl-4 text-xs text-destructive" data-testid="pp-validation-errors">
                {fieldErrors.map((e) => (
                  <li key={`${e.field}-${e.message}`}>
                    {e.field}: {e.message}
                  </li>
                ))}
              </ul>
            )}
            {error && (
              <p role="alert" className="text-sm text-destructive" data-testid="pp-server-error">
                {error}
              </p>
            )}
          </div>
          <div className="flex justify-end gap-2 border-t px-6 py-4">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || draft.name.trim() === ""}>
              {submitting ? "Saving…" : isEdit ? "Save policy pack" : "Create policy pack"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * BudgetFields is the shared budget input grid used by the policy pack
 * dialog and the scan form.
 */
export function BudgetFields({
  idPrefix,
  value,
  onChange,
  disabled = false,
}: {
  idPrefix: string;
  value: BudgetDraft;
  onChange: (next: BudgetDraft) => void;
  disabled?: boolean;
}) {
  const patch = (field: keyof BudgetDraft) => (event: React.ChangeEvent<HTMLInputElement>) =>
    onChange({ ...value, [field]: event.target.value });
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      <FlowField id={`${idPrefix}-cost`} label="Max cost (USD)" hint='Decimal, e.g. "5" or "2.50".'>
        <Input id={`${idPrefix}-cost`} value={value.maxCostUsd} onChange={patch("maxCostUsd")} placeholder="5" disabled={disabled} className="font-mono" />
      </FlowField>
      <FlowField id={`${idPrefix}-tokens`} label="Max tokens" hint="Total LLM tokens (input + output).">
        <Input id={`${idPrefix}-tokens`} type="number" min={0} value={value.maxTokens} onChange={patch("maxTokens")} disabled={disabled} />
      </FlowField>
      <FlowField id={`${idPrefix}-runtime`} label="Max runtime" hint='Duration, e.g. "2h".'>
        <Input id={`${idPrefix}-runtime`} value={value.maxRuntime} onChange={patch("maxRuntime")} placeholder="2h" disabled={disabled} className="font-mono" />
      </FlowField>
      <FlowField id={`${idPrefix}-model-jobs`} label="Max model jobs" hint="Sub-agent runs per scan run.">
        <Input id={`${idPrefix}-model-jobs`} type="number" min={0} value={value.maxModelJobs} onChange={patch("maxModelJobs")} disabled={disabled} />
      </FlowField>
      <FlowField id={`${idPrefix}-validation-jobs`} label="Max validation jobs" hint="Post-script (validation/PoC) runs.">
        <Input id={`${idPrefix}-validation-jobs`} type="number" min={0} value={value.maxValidationJobs} onChange={patch("maxValidationJobs")} disabled={disabled} />
      </FlowField>
      <FlowField id={`${idPrefix}-findings`} label="Max findings" hint="Persisted findings cap.">
        <Input id={`${idPrefix}-findings`} type="number" min={0} value={value.maxFindings} onChange={patch("maxFindings")} disabled={disabled} />
      </FlowField>
    </div>
  );
}
