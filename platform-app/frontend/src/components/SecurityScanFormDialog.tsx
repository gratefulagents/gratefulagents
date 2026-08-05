import { create } from "@bufbuild/protobuf";
import { useState } from "react";
import { CalendarClock, Crosshair, GitBranch, ListChecks, Loader2, ShieldAlert, SlidersHorizontal } from "lucide-react";

import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Chip,
  FlowField,
  FlowSwitchRow,
  OptionRow,
  OptionRows,
  Segmented,
} from "@/components/create-flow/create-flow";
import { RunDefaultsRows } from "@/components/run-defaults/RunDefaultsRows";
import { TriggerPolicyRows } from "@/components/run-defaults/TriggerPolicyRows";
import { buildCronRequest, emptyDefaults, hasExplicitCredentials } from "@/components/run-defaults/helpers";
import { resolvedTriggerPolicies } from "@/components/TriggerDefaultsDialog";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import {
  CreateSecurityScanRequestSchema,
  SecurityScanConfigSpecSchema,
  SecurityScanDedupeConfigSchema,
  SecurityScanScopeConfigSchema,
  SecurityScanTaskConfigSchema,
  SecurityPostScriptConfigSchema,
  SecurityRankerConfigSchema,
  UpdateSecurityScanRequestSchema,
  type AgentRunDefaults,
  type SecurityScanConfig,
  type TriggerPolicies,
} from "@/rpc/platform/service_pb";

const SCHEDULE_PRESETS = ["@daily", "@weekly", "0 3 * * *", "0 3 * * 1"];
const SEVERITY_OPTIONS = ["critical", "high", "medium", "low", "info"] as const;

const selectClass =
  "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

type TaskState = {
  name: string;
  objective: string;
  category: string;
  role: string;
  model: string;
  dependsOn: string;
  maxFindings: string;
};

type RankerState = { name: string; rules: string };
type PostScriptState = { name: string; prompt: string; runOn: string };

type SpecState = {
  name: string;
  repoUrl: string;
  baseBranch: string;
  revision: string;
  additionalRepos: string;
  schedule: string;
  timeZone: string;
  concurrencyPolicy: string;
  suspend: boolean;
  focus: string;
  includePaths: string;
  excludePaths: string;
  languages: string;
  minSeverity: string;
  failOnSeverity: string;
  parallelism: string;
  maxRuntime: string;
  dedupeEnabled: boolean;
  dedupeThreshold: string;
};

function splitList(value: string, separator: RegExp): string[] {
  return value
    .split(separator)
    .map((v) => v.trim())
    .filter(Boolean);
}

function initialSpec(config?: SecurityScanConfig): SpecState {
  const spec = config?.spec;
  return {
    name: config?.name ?? "",
    repoUrl: spec?.repoUrl ?? "",
    baseBranch: spec?.baseBranch ?? "",
    revision: spec?.revision ?? "",
    additionalRepos: spec?.additionalRepos.join("\n") ?? "",
    schedule: spec?.schedule ?? "",
    timeZone: spec?.timeZone ?? "",
    concurrencyPolicy: spec?.concurrencyPolicy || "Forbid",
    suspend: spec?.suspend ?? false,
    focus: spec?.scope?.focus ?? "",
    includePaths: spec?.scope?.includePaths.join(", ") ?? "",
    excludePaths: spec?.scope?.excludePaths.join(", ") ?? "",
    languages: spec?.scope?.languages.join(", ") ?? "",
    minSeverity: spec?.minSeverity ?? "",
    failOnSeverity: spec?.failOnSeverity ?? "",
    parallelism: spec?.parallelism ? String(spec.parallelism) : "",
    maxRuntime: spec?.maxRuntime ?? "",
    dedupeEnabled: spec?.dedupe ? spec.dedupe.enabled : true,
    dedupeThreshold: spec?.dedupe?.similarityThresholdPermille
      ? String(spec.dedupe.similarityThresholdPermille)
      : "",
  };
}

function initialTasks(config?: SecurityScanConfig): TaskState[] {
  return (config?.spec?.workflow ?? []).map((t) => ({
    name: t.name,
    objective: t.objective,
    category: t.category,
    role: t.role,
    model: t.model,
    dependsOn: t.dependsOn.join(", "),
    maxFindings: t.maxFindings ? String(t.maxFindings) : "",
  }));
}

function initialRankers(config?: SecurityScanConfig): RankerState[] {
  return (config?.spec?.severityRankers ?? []).map((r) => ({ name: r.name, rules: r.rules }));
}

function initialPostScripts(config?: SecurityScanConfig): PostScriptState[] {
  return (config?.spec?.postScripts ?? []).map((p) => ({
    name: p.name,
    prompt: p.prompt,
    runOn: p.runOn || "all",
  }));
}

function configPolicySource(config?: SecurityScanConfig) {
  if (!config) return undefined;
  return {
    namespace: config.namespace,
    defaults: config.spec?.defaults,
    permissionMode: "",
    egressMode: "",
    mcpPolicyDefaultAction: "",
    mcpPolicyAllowedServers: [],
  };
}

/** Best guess at whether an existing scan uses the caller's saved credentials. */
export function scanConfigUsesSavedCredentials(config: SecurityScanConfig): boolean {
  const defaults = config.spec?.defaults;
  return !defaults || !hasExplicitCredentials(defaults);
}

function scheduleSummary(spec: SpecState): string {
  const parts = [spec.schedule.trim() ? spec.timeZone.trim() || "UTC" : "runs once"];
  parts.push(spec.concurrencyPolicy === "Allow" ? "overlaps allowed" : "overlaps skipped");
  if (spec.suspend) parts.push("paused");
  return parts.join(" · ");
}

function scopeSummary(spec: SpecState): string {
  const parts: string[] = [];
  if (spec.focus.trim()) parts.push("focus set");
  if (spec.includePaths.trim() || spec.excludePaths.trim()) parts.push("path filters");
  if (spec.languages.trim()) parts.push(spec.languages.trim());
  return parts.length ? parts.join(" · ") : "Whole repository";
}

function reportingSummary(spec: SpecState): string {
  const parts = [`min ${spec.minSeverity || "low"}`];
  if (spec.failOnSeverity) parts.push(`fail on ${spec.failOnSeverity}`);
  parts.push(spec.dedupeEnabled ? "dedupe on" : "dedupe off");
  return parts.join(" · ");
}

/**
 * Create/edit dialog for SecurityScan triggers. Pass `config` to edit an
 * existing scan; omit it to create a new one.
 */
export function SecurityScanFormDialog({
  config,
  trigger,
  defaultOpen = false,
  onSaved,
}: {
  config?: SecurityScanConfig;
  trigger: React.ReactElement;
  defaultOpen?: boolean;
  onSaved?: (config: SecurityScanConfig) => void;
}) {
  const isEdit = Boolean(config);
  const [open, setOpen] = useState(defaultOpen);
  const [spec, setSpec] = useState<SpecState>(() => initialSpec(config));
  const [tasks, setTasks] = useState<TaskState[]>(() => initialTasks(config));
  const [rankers, setRankers] = useState<RankerState[]>(() => initialRankers(config));
  const [postScripts, setPostScripts] = useState<PostScriptState[]>(() => initialPostScripts(config));
  const [defaults, setDefaults] = useState<AgentRunDefaults>(
    () => config?.spec?.defaults ?? emptyDefaults(),
  );
  const [policies, setPolicies] = useState<TriggerPolicies>(() =>
    resolvedTriggerPolicies(configPolicySource(config)),
  );
  const [useSavedCredentials, setUseSavedCredentials] = useState(() =>
    config ? scanConfigUsesSavedCredentials(config) : true,
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function update<K extends keyof SpecState>(field: K, value: SpecState[K]) {
    setSpec((prev) => ({ ...prev, [field]: value }));
  }

  function reset() {
    setSpec(initialSpec(config));
    setTasks(initialTasks(config));
    setRankers(initialRankers(config));
    setPostScripts(initialPostScripts(config));
    setDefaults(config?.spec?.defaults ?? emptyDefaults());
    setPolicies(resolvedTriggerPolicies(configPolicySource(config)));
    setUseSavedCredentials(config ? scanConfigUsesSavedCredentials(config) : true);
    setError(null);
  }

  function buildSpec() {
    // Reuse the shared trigger-defaults normalization from the Cron form.
    const { defaults: normalizedDefaults } = buildCronRequest({
      namespace: "",
      name: "",
      schedule: "",
      timeZone: "",
      suspend: false,
      concurrencyPolicy: "",
      prompt: "-",
      defaults,
      useSavedCredentials,
    });
    const scope = create(SecurityScanScopeConfigSchema, {
      focus: spec.focus.trim(),
      includePaths: splitList(spec.includePaths, /[,\n]/),
      excludePaths: splitList(spec.excludePaths, /[,\n]/),
      languages: splitList(spec.languages, /[,\n]/),
    });
    return create(SecurityScanConfigSpecSchema, {
      repoUrl: spec.repoUrl.trim(),
      baseBranch: spec.baseBranch.trim(),
      revision: spec.revision.trim(),
      additionalRepos: splitList(spec.additionalRepos, /\n/),
      scope,
      workflow: tasks.map((t) =>
        create(SecurityScanTaskConfigSchema, {
          name: t.name.trim(),
          objective: t.objective.trim(),
          category: t.category.trim(),
          role: t.role.trim(),
          model: t.model.trim(),
          dependsOn: splitList(t.dependsOn, /[,\n]/),
          maxFindings: t.maxFindings.trim() ? Number(t.maxFindings) : 0,
        }),
      ),
      parallelism: spec.parallelism.trim() ? Number(spec.parallelism) : 0,
      severityRankers: rankers.map((r) =>
        create(SecurityRankerConfigSchema, { name: r.name.trim(), rules: r.rules }),
      ),
      postScripts: postScripts.map((p) =>
        create(SecurityPostScriptConfigSchema, {
          name: p.name.trim(),
          prompt: p.prompt,
          runOn: p.runOn === "all" ? "" : p.runOn,
        }),
      ),
      dedupe: create(SecurityScanDedupeConfigSchema, {
        enabled: spec.dedupeEnabled,
        similarityThresholdPermille: spec.dedupeThreshold.trim() ? Number(spec.dedupeThreshold) : 0,
      }),
      minSeverity: spec.minSeverity,
      failOnSeverity: spec.failOnSeverity,
      schedule: spec.schedule.trim(),
      timeZone: spec.timeZone.trim(),
      suspend: spec.suspend,
      concurrencyPolicy: spec.concurrencyPolicy,
      defaults: normalizedDefaults,
      maxRuntime: spec.maxRuntime.trim(),
    });
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    if (!spec.repoUrl.trim()) {
      setError("Give the scan a repository URL to analyze.");
      return;
    }
    setSubmitting(true);
    try {
      const requestSpec = buildSpec();
      const saved = isEdit
        ? await client.updateSecurityScan(
            create(UpdateSecurityScanRequestSchema, {
              namespace: config?.namespace ?? "",
              name: config?.name ?? "",
              spec: requestSpec,
              useSavedCredentials,
              policies,
            }),
          )
        : await client.createSecurityScan(
            create(CreateSecurityScanRequestSchema, {
              name: spec.name.trim(),
              spec: requestSpec,
              useSavedCredentials,
              policies,
            }),
          );
      setOpen(false);
      reset();
      onSaved?.(saved);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save security scan");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <ShieldAlert className="size-4" />
              </span>
              <DialogTitle className="text-base">
                {isEdit ? `Edit ${config?.name}` : "New security scan"}
              </DialogTitle>
            </div>
            <DialogDescription>
              {isEdit
                ? "Saving replaces the scan's spec with the values below."
                : "Scan a repository for vulnerabilities, once or on a schedule."}
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <FlowField id="scan-repo-url" label="Repository URL" required>
              <Input
                id="scan-repo-url"
                value={spec.repoUrl}
                onChange={(event) => update("repoUrl", event.target.value)}
                placeholder="https://github.com/acme/payments.git"
                className="font-mono"
                autoFocus
                required
              />
            </FlowField>

            <FlowField
              id="scan-schedule"
              label="Schedule"
              hint="Optional — leave empty to scan once per configuration change."
            >
              <Input
                id="scan-schedule"
                value={spec.schedule}
                onChange={(event) => update("schedule", event.target.value)}
                placeholder="0 3 * * *"
                className="font-mono"
              />
              <div className="flex flex-wrap gap-1.5 pt-1.5">
                {SCHEDULE_PRESETS.map((preset) => (
                  <Chip
                    key={preset}
                    mono
                    selected={spec.schedule === preset}
                    onSelect={() => update("schedule", spec.schedule === preset ? "" : preset)}
                  >
                    {preset}
                  </Chip>
                ))}
              </div>
            </FlowField>

            {!isEdit ? (
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField id="scan-name" label="Name" hint="Optional — derived automatically if empty.">
                  <Input
                    id="scan-name"
                    value={spec.name}
                    onChange={(event) => update("name", event.target.value)}
                    placeholder="nightly-payments-scan"
                  />
                </FlowField>
              </div>
            ) : null}

            <OptionRows label="Options" className="pt-1">
              <OptionRow
                icon={CalendarClock}
                title="Scheduling"
                summary={scheduleSummary(spec)}
                modified={Boolean(spec.timeZone.trim()) || spec.concurrencyPolicy === "Allow" || spec.suspend}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="scan-time-zone" label="Time zone" hint="IANA name, empty = UTC.">
                    <Input
                      id="scan-time-zone"
                      value={spec.timeZone}
                      onChange={(event) => update("timeZone", event.target.value)}
                      placeholder="America/New_York"
                    />
                  </FlowField>
                </div>
                <FlowSwitchRow
                  label="If the previous scan is still running"
                  control={
                    <Segmented
                      aria-label="Concurrency policy"
                      value={spec.concurrencyPolicy === "Allow" ? "Allow" : "Forbid"}
                      onChange={(policy) => update("concurrencyPolicy", policy)}
                      options={[
                        { value: "Forbid", label: "Skip" },
                        { value: "Allow", label: "Run anyway" },
                      ]}
                    />
                  }
                />
                <FlowSwitchRow
                  id="scan-suspend"
                  label="Suspend"
                  hint="Pause new scan runs without deleting the configuration."
                  control={
                    <Switch
                      id="scan-suspend"
                      checked={spec.suspend}
                      onCheckedChange={(checked) => update("suspend", checked)}
                    />
                  }
                />
              </OptionRow>

              <OptionRow
                icon={GitBranch}
                title="Scan target"
                summary={[spec.baseBranch.trim() || "main", spec.revision.trim() || "branch head"].join(" · ")}
                modified={Boolean(spec.baseBranch.trim() || spec.revision.trim() || spec.additionalRepos.trim())}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="scan-base-branch" label="Base branch" hint="Empty = main.">
                    <Input
                      id="scan-base-branch"
                      value={spec.baseBranch}
                      onChange={(event) => update("baseBranch", event.target.value)}
                      placeholder="main"
                    />
                  </FlowField>
                  <FlowField id="scan-revision" label="Revision" hint="Optional commit pin; empty = branch head.">
                    <Input
                      id="scan-revision"
                      value={spec.revision}
                      onChange={(event) => update("revision", event.target.value)}
                      placeholder="a1b2c3d"
                      className="font-mono"
                    />
                  </FlowField>
                </div>
                <FlowField
                  id="scan-additional-repos"
                  label="Additional repositories"
                  hint="Dependency repositories cloned alongside the target, one URL per line."
                >
                  <Textarea
                    id="scan-additional-repos"
                    value={spec.additionalRepos}
                    onChange={(event) => update("additionalRepos", event.target.value)}
                    className="min-h-16 font-mono"
                    placeholder="https://github.com/acme/shared-lib.git"
                  />
                </FlowField>
              </OptionRow>

              <OptionRow
                icon={Crosshair}
                title="Scope"
                summary={scopeSummary(spec)}
                modified={Boolean(
                  spec.focus.trim() || spec.includePaths.trim() || spec.excludePaths.trim() || spec.languages.trim(),
                )}
              >
                <FlowField
                  id="scan-focus"
                  label="Focus"
                  hint="Free-form guidance about what the scan should prioritize."
                >
                  <Textarea
                    id="scan-focus"
                    value={spec.focus}
                    onChange={(event) => update("focus", event.target.value)}
                    className="min-h-16"
                    placeholder="Prioritize the payment authorization flow."
                  />
                </FlowField>
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="scan-include-paths" label="Include paths" hint="Comma-separated globs; empty = all.">
                    <Input
                      id="scan-include-paths"
                      value={spec.includePaths}
                      onChange={(event) => update("includePaths", event.target.value)}
                      placeholder="internal/**, cmd/**"
                      className="font-mono"
                    />
                  </FlowField>
                  <FlowField id="scan-exclude-paths" label="Exclude paths" hint="Comma-separated globs to skip.">
                    <Input
                      id="scan-exclude-paths"
                      value={spec.excludePaths}
                      onChange={(event) => update("excludePaths", event.target.value)}
                      placeholder="vendor/**, testdata/**"
                      className="font-mono"
                    />
                  </FlowField>
                </div>
                <FlowField id="scan-languages" label="Languages" hint="Comma-separated; empty = all languages.">
                  <Input
                    id="scan-languages"
                    value={spec.languages}
                    onChange={(event) => update("languages", event.target.value)}
                    placeholder="go, typescript"
                  />
                </FlowField>
              </OptionRow>

              <OptionRow
                icon={ShieldAlert}
                title="Reporting"
                summary={reportingSummary(spec)}
                modified={Boolean(
                  spec.minSeverity ||
                    spec.failOnSeverity ||
                    !spec.dedupeEnabled ||
                    spec.dedupeThreshold.trim() ||
                    spec.parallelism.trim() ||
                    spec.maxRuntime.trim(),
                )}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="scan-min-severity"
                    label="Minimum severity"
                    hint="Findings below this severity are dropped from the report."
                  >
                    <select
                      id="scan-min-severity"
                      className={selectClass}
                      value={spec.minSeverity}
                      onChange={(event) => update("minSeverity", event.target.value)}
                    >
                      <option value="">low (default)</option>
                      {SEVERITY_OPTIONS.map((s) => (
                        <option key={s} value={s}>{s}</option>
                      ))}
                    </select>
                  </FlowField>
                  <FlowField
                    id="scan-fail-on-severity"
                    label="Fail on severity"
                    hint="Mark the scan not-ready when findings at or above this severity exist."
                  >
                    <select
                      id="scan-fail-on-severity"
                      className={selectClass}
                      value={spec.failOnSeverity}
                      onChange={(event) => update("failOnSeverity", event.target.value)}
                    >
                      <option value="">off</option>
                      {SEVERITY_OPTIONS.map((s) => (
                        <option key={s} value={s}>{s}</option>
                      ))}
                    </select>
                  </FlowField>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="scan-parallelism"
                    label="Parallelism"
                    hint="Concurrent workflow tasks (1-16); empty = 4."
                  >
                    <Input
                      id="scan-parallelism"
                      type="number"
                      min={1}
                      max={16}
                      value={spec.parallelism}
                      onChange={(event) => update("parallelism", event.target.value)}
                      placeholder="4"
                    />
                  </FlowField>
                  <FlowField
                    id="scan-max-runtime"
                    label="Max runtime"
                    hint="Cap per scan run, e.g. 2h; empty = run defaults."
                  >
                    <Input
                      id="scan-max-runtime"
                      value={spec.maxRuntime}
                      onChange={(event) => update("maxRuntime", event.target.value)}
                      placeholder="2h"
                      className="font-mono"
                    />
                  </FlowField>
                </div>
                <FlowSwitchRow
                  id="scan-dedupe"
                  label="Deduplicate findings"
                  hint="Suppress near-duplicate findings across runs."
                  control={
                    <Switch
                      id="scan-dedupe"
                      checked={spec.dedupeEnabled}
                      onCheckedChange={(checked) => update("dedupeEnabled", checked)}
                    />
                  }
                />
                {spec.dedupeEnabled && (
                  <div className="grid gap-4 sm:grid-cols-2">
                    <FlowField
                      id="scan-dedupe-threshold"
                      label="Similarity threshold (permille)"
                      hint="0-1000; empty = 820 (0.82 similarity)."
                    >
                      <Input
                        id="scan-dedupe-threshold"
                        type="number"
                        min={0}
                        max={1000}
                        value={spec.dedupeThreshold}
                        onChange={(event) => update("dedupeThreshold", event.target.value)}
                        placeholder="820"
                      />
                    </FlowField>
                  </div>
                )}
              </OptionRow>

              <OptionRow
                icon={ListChecks}
                title="Workflow tasks"
                summary={tasks.length ? `${tasks.length} custom task${tasks.length === 1 ? "" : "s"}` : "Default workflow"}
                modified={tasks.length > 0}
              >
                <p className="text-xs text-muted-foreground">
                  Leave empty to use the built-in vulnerability-hunting workflow. Custom tasks
                  replace it entirely; depends_on must reference other task names.
                </p>
                {tasks.map((task, index) => (
                  <div key={index} className="space-y-3 rounded-md border p-3">
                    <div className="grid gap-3 sm:grid-cols-2">
                      <FlowField id={`scan-task-name-${index}`} label="Task name">
                        <Input
                          id={`scan-task-name-${index}`}
                          value={task.name}
                          onChange={(event) =>
                            setTasks((prev) =>
                              prev.map((t, i) => (i === index ? { ...t, name: event.target.value } : t)),
                            )
                          }
                          placeholder="injection-hunt"
                          className="font-mono"
                        />
                      </FlowField>
                      <FlowField id={`scan-task-category-${index}`} label="Category">
                        <Input
                          id={`scan-task-category-${index}`}
                          value={task.category}
                          onChange={(event) =>
                            setTasks((prev) =>
                              prev.map((t, i) => (i === index ? { ...t, category: event.target.value } : t)),
                            )
                          }
                          placeholder="injection"
                        />
                      </FlowField>
                    </div>
                    <FlowField id={`scan-task-objective-${index}`} label="Objective">
                      <Textarea
                        id={`scan-task-objective-${index}`}
                        value={task.objective}
                        onChange={(event) =>
                          setTasks((prev) =>
                            prev.map((t, i) => (i === index ? { ...t, objective: event.target.value } : t)),
                          )
                        }
                        className="min-h-16"
                        placeholder="Hunt for SQL injection in the API layer."
                      />
                    </FlowField>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <FlowField id={`scan-task-role-${index}`} label="Role" hint="Empty = security-reviewer.">
                        <Input
                          id={`scan-task-role-${index}`}
                          value={task.role}
                          onChange={(event) =>
                            setTasks((prev) =>
                              prev.map((t, i) => (i === index ? { ...t, role: event.target.value } : t)),
                            )
                          }
                          placeholder="vulnerability-hunter"
                        />
                      </FlowField>
                      <FlowField id={`scan-task-model-${index}`} label="Model" hint="Empty = trigger default.">
                        <Input
                          id={`scan-task-model-${index}`}
                          value={task.model}
                          onChange={(event) =>
                            setTasks((prev) =>
                              prev.map((t, i) => (i === index ? { ...t, model: event.target.value } : t)),
                            )
                          }
                        />
                      </FlowField>
                    </div>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <FlowField
                        id={`scan-task-depends-${index}`}
                        label="Depends on"
                        hint="Comma-separated task names."
                      >
                        <Input
                          id={`scan-task-depends-${index}`}
                          value={task.dependsOn}
                          onChange={(event) =>
                            setTasks((prev) =>
                              prev.map((t, i) => (i === index ? { ...t, dependsOn: event.target.value } : t)),
                            )
                          }
                          className="font-mono"
                        />
                      </FlowField>
                      <FlowField
                        id={`scan-task-max-findings-${index}`}
                        label="Max findings"
                        hint="Empty or 0 = unlimited."
                      >
                        <Input
                          id={`scan-task-max-findings-${index}`}
                          type="number"
                          min={0}
                          value={task.maxFindings}
                          onChange={(event) =>
                            setTasks((prev) =>
                              prev.map((t, i) => (i === index ? { ...t, maxFindings: event.target.value } : t)),
                            )
                          }
                        />
                      </FlowField>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setTasks((prev) => prev.filter((_, i) => i !== index))}
                    >
                      Remove task
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setTasks((prev) => [
                      ...prev,
                      { name: "", objective: "", category: "", role: "", model: "", dependsOn: "", maxFindings: "" },
                    ])
                  }
                >
                  Add workflow task
                </Button>
              </OptionRow>

              <OptionRow
                icon={SlidersHorizontal}
                title="Rankers & post-scripts"
                summary={
                  rankers.length || postScripts.length
                    ? `${rankers.length} ranker${rankers.length === 1 ? "" : "s"} · ${postScripts.length} post-script${postScripts.length === 1 ? "" : "s"}`
                    : "None"
                }
                modified={rankers.length > 0 || postScripts.length > 0}
              >
                <p className="text-xs text-muted-foreground">
                  Severity rankers add your ranking rules to the scan prompt; post-scripts run a
                  prompt against each matching finding after the scan.
                </p>
                {rankers.map((ranker, index) => (
                  <div key={index} className="space-y-3 rounded-md border p-3">
                    <FlowField id={`scan-ranker-name-${index}`} label="Ranker name">
                      <Input
                        id={`scan-ranker-name-${index}`}
                        value={ranker.name}
                        onChange={(event) =>
                          setRankers((prev) =>
                            prev.map((r, i) => (i === index ? { ...r, name: event.target.value } : r)),
                          )
                        }
                        placeholder="payments-rules"
                      />
                    </FlowField>
                    <FlowField id={`scan-ranker-rules-${index}`} label="Ranking rules">
                      <Textarea
                        id={`scan-ranker-rules-${index}`}
                        value={ranker.rules}
                        onChange={(event) =>
                          setRankers((prev) =>
                            prev.map((r, i) => (i === index ? { ...r, rules: event.target.value } : r)),
                          )
                        }
                        className="min-h-16"
                        placeholder="Any authentication bypass is critical."
                      />
                    </FlowField>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setRankers((prev) => prev.filter((_, i) => i !== index))}
                    >
                      Remove ranker
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setRankers((prev) => [...prev, { name: "", rules: "" }])}
                >
                  Add severity ranker
                </Button>
                {postScripts.map((script, index) => (
                  <div key={index} className="space-y-3 rounded-md border p-3">
                    <div className="grid gap-3 sm:grid-cols-2">
                      <FlowField id={`scan-post-name-${index}`} label="Post-script name">
                        <Input
                          id={`scan-post-name-${index}`}
                          value={script.name}
                          onChange={(event) =>
                            setPostScripts((prev) =>
                              prev.map((p, i) => (i === index ? { ...p, name: event.target.value } : p)),
                            )
                          }
                          placeholder="write-poc"
                        />
                      </FlowField>
                      <FlowField id={`scan-post-runon-${index}`} label="Run against">
                        <select
                          id={`scan-post-runon-${index}`}
                          className={selectClass}
                          value={script.runOn}
                          onChange={(event) =>
                            setPostScripts((prev) =>
                              prev.map((p, i) => (i === index ? { ...p, runOn: event.target.value } : p)),
                            )
                          }
                        >
                          <option value="all">all findings</option>
                          <option value="confirmed">confirmed findings</option>
                          <option value="high-and-above">high and above</option>
                        </select>
                      </FlowField>
                    </div>
                    <FlowField id={`scan-post-prompt-${index}`} label="Prompt">
                      <Textarea
                        id={`scan-post-prompt-${index}`}
                        value={script.prompt}
                        onChange={(event) =>
                          setPostScripts((prev) =>
                            prev.map((p, i) => (i === index ? { ...p, prompt: event.target.value } : p)),
                          )
                        }
                        className="min-h-16"
                        placeholder="Write a proof-of-concept for this finding."
                      />
                    </FlowField>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setPostScripts((prev) => prev.filter((_, i) => i !== index))}
                    >
                      Remove post-script
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setPostScripts((prev) => [...prev, { name: "", prompt: "", runOn: "all" }])}
                >
                  Add post-script
                </Button>
              </OptionRow>

              <RunDefaultsRows
                idPrefix="scan-defaults"
                resourceNamespace={config?.namespace}
                value={defaults}
                onChange={setDefaults}
                useSavedCredentials={useSavedCredentials}
                onUseSavedCredentialsChange={setUseSavedCredentials}
                hideRepository
              />
              <TriggerPolicyRows
                idPrefix="scan-policies"
                policies={policies}
                onPoliciesChange={setPolicies}
                runtimeProfileRef={defaults.runtimeProfileRef}
                onRuntimeProfileRefChange={(ref) =>
                  setDefaults((prev) => ({ ...prev, runtimeProfileRef: ref }))
                }
                mcpPolicyRef={defaults.mcpPolicyRef}
                onMcpPolicyRefChange={(ref) => setDefaults((prev) => ({ ...prev, mcpPolicyRef: ref }))}
              />
            </OptionRows>

            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>

          <div className="flex items-center justify-end gap-2 border-t px-6 py-4">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting
                ? isEdit
                  ? "Saving…"
                  : "Creating…"
                : isEdit
                  ? "Save changes"
                  : "Create scan"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
