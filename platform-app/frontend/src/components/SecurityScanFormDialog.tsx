import { clone, create } from "@bufbuild/protobuf";
import { useEffect, useState } from "react";
import { Bell, CalendarClock, Crosshair, GitBranch, GitPullRequest, ListChecks, Loader2, Route, ShieldAlert, ShieldCheck, SlidersHorizontal } from "lucide-react";

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
import {
  BudgetFields,
  budgetDraftIsZero,
  budgetsFromDraft,
  budgetsToDraft,
  packBudgetSummary,
  packRetentionSummary,
  validateBudgetDraft,
  type BudgetDraft,
} from "@/components/SecurityPolicyPackDialog";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import { applyModelDefaults, hasActiveModelDefaults } from "@/lib/modelDefaults";
import { useMyModelDefaults } from "@/hooks/useMyModelDefaults";
import {
  AgentRunDefaultsSchema,
  CreateSecurityScanRequestSchema,
  SecurityScanConfigSpecSchema,
  SecurityScanChecksConfigSchema,
  SecurityScanDedupeConfigSchema,
  SecurityScanExecutionConfigSchema,
  SecurityScanNotificationRuleConfigSchema,
  SecurityScanScopeConfigSchema,
  SecurityScanTaskConfigSchema,
  SecurityScanTriggersConfigSchema,
  SecurityPostScriptConfigSchema,
  SecurityRankerConfigSchema,
  UpdateSecurityScanRequestSchema,
  type AgentRunDefaults,
  type SecurityScanConfig,
  type SecurityScanTaskConfig,
  type SecurityPolicyPackResource,
  type SecurityPostScriptResource,
  type SecurityProgramResource,
  type SecurityRankerResource,
  type SecurityWorkflowResource,
  type TriggerPolicies,
} from "@/rpc/platform/service_pb";

const SCHEDULE_PRESETS = ["@daily", "@weekly", "0 3 * * *", "0 3 * * 1"];
const SEVERITY_OPTIONS = ["critical", "high", "medium", "low", "info"] as const;

const selectClass =
  "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

type TaskState = {
  /** Original proto task in edit/duplicate mode; carries advanced fields this inline editor doesn't expose. */
  base?: SecurityScanTaskConfig;
  name: string;
  objective: string;
  category: string;
  role: string;
  model: string;
  dependsOn: string;
};

type RankerState = { name: string; rules: string };
type PostScriptState = { name: string; prompt: string; runOn: string };

type NotificationRuleState = {
  name: string;
  minSeverity: string;
  notifyOn: string;
  slackWebhookSecretRef: string;
  githubIssues: boolean;
  githubRepositoryRef: string;
  linearApiKeySecretRef: string;
  linearTeamId: string;
};

type SpecState = {
  name: string;
  targetType: "repository" | "website";
  repoUrl: string;
  targetUrl: string;
  baseBranch: string;
  revision: string;
  additionalRepos: string;
  schedule: string;
  manualOnly: boolean;
  timeZone: string;
  concurrencyPolicy: string;
  suspend: boolean;
  focus: string;
  includePaths: string;
  excludePaths: string;
  languages: string;
  authorizedNetworkTargets: string;
  minSeverity: string;
  failOnSeverity: string;
  parallelism: string;
  maxRuntime: string;
  dedupeEnabled: boolean;
  dedupeThreshold: string;
  workflowRef: string;
  rankerRefs: string[];
  postScriptRefs: string[];
  triggersRepositoryRef: string;
  onPullRequest: boolean;
  onPush: boolean;
  triggerBranches: string;
  diffScope: boolean;
  allowForks: boolean;
  checksEnabled: boolean;
  includeFindingSummaries: boolean;
  uploadSarif: boolean;
  policyPackRef: string;
  securityProgramRef: string;
  budgets: BudgetDraft;
  /** "" = deterministic default; "coordinator" = single seeded orchestrating run. */
  executionMode: string;
  taskMaxRetries: string;
  retryBackoff: string;
  parameterValues: { key: string; value: string }[];
};

function splitList(value: string, separator: RegExp): string[] {
  return value
    .split(separator)
    .map((v) => v.trim())
    .filter(Boolean);
}

function normalizeWebsiteTarget(value: string): string {
  const trimmed = value.trim();
  if (!trimmed || /^https?:\/\//i.test(trimmed)) return trimmed;
  return `https://${trimmed}`;
}

function initialSpec(config?: SecurityScanConfig): SpecState {
  const spec = config?.spec;
  return {
    name: config?.name ?? "",
    targetType: spec?.targetUrl ? "website" : "repository",
    repoUrl: spec?.repoUrl ?? "",
    targetUrl: spec?.targetUrl ?? "",
    baseBranch: spec?.baseBranch ?? "",
    revision: spec?.revision ?? "",
    additionalRepos: spec?.additionalRepos.join("\n") ?? "",
    schedule: spec?.schedule ?? "",
    manualOnly: spec?.manualOnly ?? false,
    timeZone: spec?.timeZone ?? "",
    concurrencyPolicy: spec?.concurrencyPolicy || "Forbid",
    suspend: spec?.suspend ?? false,
    focus: spec?.scope?.focus ?? "",
    includePaths: spec?.scope?.includePaths.join(", ") ?? "",
    excludePaths: spec?.scope?.excludePaths.join(", ") ?? "",
    languages: spec?.scope?.languages.join(", ") ?? "",
    authorizedNetworkTargets: spec?.scope?.authorizedNetworkTargets.join(", ") ?? "",
    minSeverity: spec?.minSeverity ?? "",
    failOnSeverity: spec?.failOnSeverity ?? "",
    parallelism: spec?.parallelism ? String(spec.parallelism) : "",
    maxRuntime: spec?.maxRuntime ?? "",
    dedupeEnabled: spec?.dedupe ? spec.dedupe.enabled : true,
    dedupeThreshold: spec?.dedupe?.similarityThresholdPermille
      ? String(spec.dedupe.similarityThresholdPermille)
      : "",
    workflowRef: spec?.workflowRef ?? "",
    rankerRefs: [...(spec?.rankerRefs ?? [])],
    postScriptRefs: [...(spec?.postScriptRefs ?? [])],
    triggersRepositoryRef: spec?.triggers?.repositoryRef ?? "",
    onPullRequest: spec?.triggers?.onPullRequest ?? false,
    onPush: spec?.triggers?.onPush ?? false,
    triggerBranches: spec?.triggers?.branches.join(", ") ?? "",
    diffScope: spec?.triggers?.diffScope ?? false,
    allowForks: spec?.triggers?.allowForks ?? false,
    checksEnabled: spec?.checks?.enabled ?? false,
    includeFindingSummaries: spec?.checks?.includeFindingSummaries ?? false,
    uploadSarif: spec?.checks?.uploadSarif ?? false,
    policyPackRef: spec?.policyPackRef ?? "",
    securityProgramRef: spec?.securityProgramRef ?? "",
    budgets: budgetsToDraft(spec?.budgets),
    executionMode: spec?.execution?.mode ?? "",
    taskMaxRetries:
      spec?.execution?.taskMaxRetries !== undefined ? String(spec.execution.taskMaxRetries) : "",
    retryBackoff: spec?.execution?.retryBackoff ?? "",
    parameterValues: Object.entries(spec?.parameterValues ?? {})
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([key, value]) => ({ key, value })),
  };
}

function initialNotifications(config?: SecurityScanConfig): NotificationRuleState[] {
  return (config?.spec?.notifications ?? []).map((r) => ({
    name: r.name,
    minSeverity: r.minSeverity,
    notifyOn: r.notifyOn || "new-and-regressed",
    slackWebhookSecretRef: r.slackWebhookSecretRef,
    githubIssues: r.githubIssues,
    githubRepositoryRef: r.githubRepositoryRef,
    linearApiKeySecretRef: r.linearApiKeySecretRef,
    linearTeamId: r.linearTeamId,
  }));
}

// initialDialogSpec prefills the form from `source`; in duplicate mode the
// name is cleared so the user must choose a new one.
function initialDialogSpec(source: SecurityScanConfig | undefined, isDuplicate: boolean): SpecState {
  const spec = initialSpec(source);
  if (isDuplicate) {
    spec.name = "";
  }
  return spec;
}

function initialTasks(config?: SecurityScanConfig): TaskState[] {
  return (config?.spec?.workflow ?? []).map((t) => ({
    base: t,
    name: t.name,
    objective: t.objective,
    category: t.category,
    role: t.role,
    model: t.model,
    dependsOn: t.dependsOn.join(", "),
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
  if (spec.manualOnly) return "manual only";
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
  if (spec.authorizedNetworkTargets.trim()) parts.push("network targets authorized");
  if (spec.securityProgramRef.trim()) parts.push(`program: ${spec.securityProgramRef.trim()}`);
  return parts.length ? parts.join(" · ") : "Whole repository";
}

function reportingSummary(spec: SpecState): string {
  const parts = [`min ${spec.minSeverity || "low"}`];
  if (spec.failOnSeverity) parts.push(`fail on ${spec.failOnSeverity}`);
  parts.push(spec.dedupeEnabled ? "dedupe on" : "dedupe off");
  return parts.join(" · ");
}

function repositoryEventsSummary(spec: SpecState): string {
  const parts: string[] = [];
  if (spec.onPullRequest) parts.push("pull requests");
  if (spec.onPush) parts.push("pushes");
  if (!parts.length) return "Off";
  if (spec.diffScope) parts.push("diff scope");
  if (spec.checksEnabled) parts.push("checks");
  if (spec.allowForks) parts.push("forks allowed");
  return parts.join(" · ");
}

function notificationsSummary(rules: NotificationRuleState[]): string {
  if (!rules.length) return "Off";
  return `${rules.length} rule${rules.length === 1 ? "" : "s"}`;
}

function policyPackSummary(spec: SpecState): string {
  const parts: string[] = [];
  if (spec.policyPackRef.trim()) parts.push(spec.policyPackRef.trim());
  if (!budgetDraftIsZero(spec.budgets)) parts.push("scan budgets set");
  return parts.length ? parts.join(" · ") : "None";
}

const GO_DURATION_PATTERN = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;

function executionSummary(spec: SpecState): string {
  const parts = [spec.executionMode.trim() === "coordinator" ? "coordinator" : "deterministic"];
  const params = spec.parameterValues.filter((entry) => entry.key.trim() !== "").length;
  if (params > 0) parts.push(`${params} parameter${params === 1 ? "" : "s"}`);
  return parts.join(" · ");
}

/** Human labels for the pack fields scans may not relax. */
const ENFORCED_LABELS: Record<string, string> = {
  minSeverity: "minimum severity",
  failOnSeverity: "fail-on severity",
  dedupe: "dedupe",
  requiredCategories: "required categories",
  allowedRuntimeProfiles: "allowed runtime profiles",
  budgets: "budgets",
};

/**
 * Create/edit dialog for SecurityScan triggers. Pass `config` to edit an
 * existing scan; pass `duplicateFrom` to create a new scan pre-filled from an
 * existing one (the form is the review step — nothing is created until the
 * user confirms, and creation can never overwrite the source scan or its
 * findings); omit both to create a new one from scratch.
 */
export function SecurityScanFormDialog({
  config,
  duplicateFrom,
  initialConfig,
  trigger,
  defaultOpen = false,
  onSaved,
  onOpenChange,
}: {
  config?: SecurityScanConfig;
  duplicateFrom?: SecurityScanConfig;
  initialConfig?: SecurityScanConfig;
  trigger?: React.ReactElement;
  defaultOpen?: boolean;
  onSaved?: (config: SecurityScanConfig) => void;
  onOpenChange?: (open: boolean) => void;
}) {
  const isEdit = Boolean(config);
  const isDuplicate = !isEdit && Boolean(duplicateFrom);
  const source = config ?? duplicateFrom ?? initialConfig;
  const [open, setOpen] = useState(defaultOpen);
  const [spec, setSpec] = useState<SpecState>(() => initialDialogSpec(source, isDuplicate));
  const [tasks, setTasks] = useState<TaskState[]>(() => initialTasks(source));
  const [rankers, setRankers] = useState<RankerState[]>(() => initialRankers(source));
  const [postScripts, setPostScripts] = useState<PostScriptState[]>(() => initialPostScripts(source));
  const [notifications, setNotifications] = useState<NotificationRuleState[]>(() =>
    initialNotifications(source),
  );
  const [defaults, setDefaults] = useState<AgentRunDefaults>(
    () => source?.spec?.defaults ?? emptyDefaults(),
  );
  const [policies, setPolicies] = useState<TriggerPolicies>(() =>
    resolvedTriggerPolicies(configPolicySource(config ?? duplicateFrom)),
  );
  const [useSavedCredentials, setUseSavedCredentials] = useState(() =>
    source ? scanConfigUsesSavedCredentials(source) : true,
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // New scans include ones prefilled from an imported program target
  // (initialConfig): the template rarely sets a model, so the user's saved
  // defaults still apply to whatever it left untouched.
  const isNewScan = !config && !duplicateFrom;
  const { defaults: myModelDefaults, loaded: modelDefaultsLoaded } = useMyModelDefaults(
    open && isNewScan,
  );

  // Seed a brand-new scan's untouched run defaults from the user's saved
  // model defaults. Editing or duplicating keeps the values they were loaded
  // with, and a template that sets provider/model/reasoning wins over the
  // personal defaults.
  useEffect(() => {
    if (!open || !isNewScan || !modelDefaultsLoaded || !hasActiveModelDefaults(myModelDefaults))
      return;
    const seeded = applyModelDefaults(myModelDefaults);
    // eslint-disable-next-line react-hooks/set-state-in-effect -- one-shot prefill of untouched fields
    setDefaults((prev) => {
      if (prev.provider || prev.authMode || prev.model || prev.reasoningLevel) return prev;
      const next = clone(AgentRunDefaultsSchema, prev);
      next.provider = seeded.provider;
      next.authMode = seeded.provider === "copilot" ? "oauth" : seeded.authMode;
      next.model = seeded.model;
      next.reasoningLevel = seeded.reasoningLevel;
      return next;
    });
  }, [open, isNewScan, modelDefaultsLoaded, myModelDefaults]);
  const [libraryWorkflows, setLibraryWorkflows] = useState<SecurityWorkflowResource[]>([]);
  const [libraryRankers, setLibraryRankers] = useState<SecurityRankerResource[]>([]);
  const [libraryPostScripts, setLibraryPostScripts] = useState<SecurityPostScriptResource[]>([]);
  const [policyPacks, setPolicyPacks] = useState<SecurityPolicyPackResource[]>([]);
  const [securityPrograms, setSecurityPrograms] = useState<SecurityProgramResource[]>([]);

  // The library is optional context: load it when the dialog opens and fall
  // back to empty pickers when it cannot be listed.
  useEffect(() => {
    if (!open) return;
    void (async () => {
      try {
        const [wf, rk, ps] = await Promise.all([
          client.listSecurityWorkflows({ namespace: "" }),
          client.listSecurityRankers({ namespace: "" }),
          client.listSecurityPostScripts({ namespace: "" }),
        ]);
        setLibraryWorkflows(wf.workflows);
        setLibraryRankers(rk.rankers);
        setLibraryPostScripts(ps.postScripts);
      } catch {
        // Library pickers stay empty; inline editing keeps working.
      }
      try {
        const pp = await client.listSecurityPolicyPacks({ namespace: "" });
        setPolicyPacks(pp.policyPacks);
      } catch {
        // The pack picker stays empty; an existing ref still shows as-is.
      }
      try {
        const programList = await client.listSecurityPrograms({ namespace: "" });
        setSecurityPrograms(programList.programs);
      } catch {
        // The program picker stays empty; an existing ref still shows as-is.
      }
    })();
  }, [open]);

  function update<K extends keyof SpecState>(field: K, value: SpecState[K]) {
    setSpec((prev) => ({ ...prev, [field]: value }));
  }

  const selectedPolicyPack =
    policyPacks.find((pack) => pack.name === spec.policyPackRef.trim()) ?? null;
  const selectedSecurityProgram =
    securityPrograms.find((program) => program.name === spec.securityProgramRef.trim()) ?? null;
  const packEnforcesBudgets = selectedPolicyPack?.enforced.includes("budgets") ?? false;

  function updateNotification(index: number, patch: Partial<NotificationRuleState>) {
    setNotifications((prev) => prev.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));
  }

  function reset() {
    setSpec(initialDialogSpec(source, isDuplicate));
    setTasks(initialTasks(source));
    setRankers(initialRankers(source));
    setPostScripts(initialPostScripts(source));
    setNotifications(initialNotifications(source));
    setDefaults(source?.spec?.defaults ?? emptyDefaults());
    setPolicies(resolvedTriggerPolicies(configPolicySource(config ?? duplicateFrom)));
    setUseSavedCredentials(source ? scanConfigUsesSavedCredentials(source) : true);
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
      authorizedNetworkTargets: splitList(spec.authorizedNetworkTargets, /[,\n]/),
    });
    return create(SecurityScanConfigSpecSchema, {
      repoUrl: spec.targetType === "repository" ? spec.repoUrl.trim() : "",
      targetUrl: spec.targetType === "website" ? normalizeWebsiteTarget(spec.targetUrl) : "",
      baseBranch: spec.targetType === "repository" ? spec.baseBranch.trim() : "",
      revision: spec.targetType === "repository" ? spec.revision.trim() : "",
      additionalRepos:
        spec.targetType === "repository" ? splitList(spec.additionalRepos, /\n/) : [],
      scope,
      // workflowRef and an inline workflow are mutually exclusive; picking a
      // library workflow drops any inline tasks from the request.
      workflow: spec.workflowRef
        ? []
        : tasks.map((t) =>
            create(SecurityScanTaskConfigSchema, {
              // Advanced task fields (retries, timeout, budgets, tools,
              // output schema, fan-out) are not editable here; keep them
              // from the source spec so editing a scan never drops them.
              ...(t.base
                ? {
                    maxRetries: t.base.maxRetries,
                    timeout: t.base.timeout,
                    maxTurns: t.base.maxTurns,
                    maxCostUsd: t.base.maxCostUsd,
                    tools: t.base.tools,
                    outputSchema: t.base.outputSchema,
                    forEach: t.base.forEach,
                    maxInstances: t.base.maxInstances,
                    repeats: t.base.repeats,
                  }
                : {}),
              name: t.name.trim(),
              objective: t.objective.trim(),
              category: t.category.trim(),
              role: t.role.trim(),
              model: t.model.trim(),
              dependsOn: splitList(t.dependsOn, /[,\n]/),
            }),
          ),
      workflowRef: spec.workflowRef,
      rankerRefs: spec.rankerRefs,
      postScriptRefs: spec.postScriptRefs,
      policyPackRef: spec.policyPackRef,
      securityProgramRef: spec.securityProgramRef,
      budgets: budgetsFromDraft(spec.budgets),
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
      schedule: spec.manualOnly ? "" : spec.schedule.trim(),
      manualOnly: spec.manualOnly,
      timeZone: spec.timeZone.trim(),
      suspend: spec.manualOnly ? false : spec.suspend,
      concurrencyPolicy: spec.concurrencyPolicy,
      defaults: normalizedDefaults,
      maxRuntime: spec.maxRuntime.trim(),
      execution:
        spec.executionMode.trim() || spec.taskMaxRetries.trim() || spec.retryBackoff.trim()
          ? create(SecurityScanExecutionConfigSchema, {
              mode: spec.executionMode.trim(),
              taskMaxRetries: spec.taskMaxRetries.trim()
                ? Number(spec.taskMaxRetries)
                : undefined,
              retryBackoff: spec.retryBackoff.trim(),
            })
          : undefined,
      parameterValues: Object.fromEntries(
        spec.parameterValues
          .filter((entry) => entry.key.trim() !== "")
          .map((entry) => [entry.key.trim(), entry.value]),
      ),
      triggers:
        spec.targetType === "repository" &&
        (spec.onPullRequest || spec.onPush || spec.triggersRepositoryRef.trim())
          ? create(SecurityScanTriggersConfigSchema, {
              repositoryRef: spec.triggersRepositoryRef.trim(),
              onPullRequest: spec.onPullRequest,
              onPush: spec.onPush,
              branches: splitList(spec.triggerBranches, /[,\n]/),
              diffScope: spec.diffScope,
              allowForks: spec.allowForks,
            })
          : undefined,
      checks: spec.targetType === "repository" && spec.checksEnabled
        ? create(SecurityScanChecksConfigSchema, {
            enabled: true,
            includeFindingSummaries: spec.includeFindingSummaries,
            uploadSarif: spec.uploadSarif,
          })
        : undefined,
      notifications: notifications.map((r) =>
        create(SecurityScanNotificationRuleConfigSchema, {
          name: r.name.trim(),
          minSeverity: r.minSeverity,
          notifyOn: r.notifyOn === "new-and-regressed" ? "" : r.notifyOn,
          slackWebhookSecretRef: r.slackWebhookSecretRef.trim(),
          githubIssues: r.githubIssues,
          githubRepositoryRef: r.githubRepositoryRef.trim(),
          linearApiKeySecretRef: r.linearApiKeySecretRef.trim(),
          linearTeamId: r.linearTeamId.trim(),
        }),
      ),
    });
  }

  function validateEventConfig(): string | null {
    if ((spec.onPullRequest || spec.onPush) && !spec.triggersRepositoryRef.trim()) {
      return "Repository events need a GitHub repository connection: set the repository reference in the Repository events section.";
    }
    if (spec.checksEnabled && !spec.triggersRepositoryRef.trim()) {
      return "Publishing checks needs the repository reference in the Repository events section — its credentials post the check.";
    }
    for (const rule of notifications) {
      if (!rule.name.trim()) {
        return "Every notification rule needs a name.";
      }
      if (!rule.slackWebhookSecretRef.trim() && !rule.githubIssues && !rule.linearApiKeySecretRef.trim()) {
        return `Notification rule "${rule.name}" needs at least one channel (Slack, GitHub issues, or Linear).`;
      }
      if (Boolean(rule.linearApiKeySecretRef.trim()) !== Boolean(rule.linearTeamId.trim())) {
        return `Notification rule "${rule.name}": Linear needs both the API key secret and the team ID.`;
      }
    }
    return null;
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    if (spec.targetType === "repository" && !spec.repoUrl.trim()) {
      setError("Give the scan a repository URL to analyze.");
      return;
    }
    if (spec.targetType === "website" && !spec.targetUrl.trim()) {
      setError("Give the scan a website URL or domain to analyze.");
      return;
    }
    if (isDuplicate && !spec.name.trim()) {
      setError("Give the duplicated scan a new name.");
      return;
    }
    const eventError = validateEventConfig();
    if (eventError) {
      setError(eventError);
      return;
    }
    const budgetErrors = validateBudgetDraft("budgets", spec.budgets);
    if (budgetErrors.length > 0) {
      setError(budgetErrors.map((e) => `${e.field}: ${e.message}`).join(" "));
      return;
    }
    const taskMaxRetries = spec.taskMaxRetries.trim();
    if (taskMaxRetries !== "" && (!/^\d+$/.test(taskMaxRetries) || Number(taskMaxRetries) > 10)) {
      setError("Task max retries must be a whole number between 0 and 10.");
      return;
    }
    if (spec.retryBackoff.trim() !== "" && !GO_DURATION_PATTERN.test(spec.retryBackoff.trim())) {
      setError('Retry backoff must be a Go duration like "30s".');
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
      onOpenChange?.(false);
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
        if (submitting && !nextOpen) return;
        setOpen(nextOpen);
        onOpenChange?.(nextOpen);
        if (!nextOpen) reset();
      }}
    >
      {trigger && <DialogTrigger render={trigger} />}
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton={!submitting}
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <ShieldAlert className="size-4" />
              </span>
              <DialogTitle className="text-base">
                {isEdit
                  ? `Edit ${config?.name}`
                  : isDuplicate
                    ? `Duplicate ${duplicateFrom?.name}`
                    : "New security scan"}
              </DialogTitle>
            </div>
            <DialogDescription>
              {isEdit
                ? "Saving replaces the scan's spec with the values below."
                : isDuplicate
                  ? "Review the copied settings and pick a new name. A new scan is created; the source scan and its findings are untouched."
                  : "Scan a repository or website for vulnerabilities, once or on a schedule."}
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <FlowField id="scan-target-type" label="Target type" required>
              <select
                id="scan-target-type"
                className={selectClass}
                value={spec.targetType}
                onChange={(event) =>
                  update("targetType", event.target.value as SpecState["targetType"])
                }
              >
                <option value="repository">Repository</option>
                <option value="website">Website or domain</option>
              </select>
            </FlowField>

            {spec.targetType === "repository" ? (
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
            ) : (
              <FlowField
                id="scan-target-url"
                label="Website URL or domain"
                hint="Bare domains use HTTPS and include all subdomains. Browser, WebFetch, shell, and registered security tools remain available."
                required
              >
                <Input
                  id="scan-target-url"
                  value={spec.targetUrl}
                  onChange={(event) => update("targetUrl", event.target.value)}
                  placeholder="https://staging.example.com"
                  className="font-mono"
                  autoFocus
                  required
                />
              </FlowField>
            )}

            <FlowSwitchRow
              id="scan-manual-only"
              label="Manual-only"
              hint="Only run this configuration when you choose Run now. It will not run from saves, schedules, or repository events."
              control={
                <Switch
                  id="scan-manual-only"
                  checked={spec.manualOnly}
                  onCheckedChange={(checked) => {
                    setSpec((current) => ({
                      ...current,
                      manualOnly: checked,
                      schedule: checked ? "" : current.schedule,
                      suspend: checked ? false : current.suspend,
                    }));
                  }}
                />
              }
            />

            <FlowField
              id="scan-schedule"
              label="Schedule"
              hint={
                spec.manualOnly
                  ? "Disabled while this configuration is manual-only."
                  : "Optional — leave empty to scan once per configuration change."
              }
            >
              <Input
                id="scan-schedule"
                value={spec.schedule}
                onChange={(event) => update("schedule", event.target.value)}
                placeholder="0 3 * * *"
                className="font-mono"
                disabled={spec.manualOnly}
              />
              {!spec.manualOnly && (
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
              )}
            </FlowField>

            {!isEdit ? (
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField
                  id="scan-name"
                  label="Name"
                  required={isDuplicate}
                  hint={
                    isDuplicate
                      ? "Required — the duplicate needs its own name."
                      : "Optional — derived automatically if empty."
                  }
                >
                  <Input
                    id="scan-name"
                    value={spec.name}
                    onChange={(event) => update("name", event.target.value)}
                    placeholder={isDuplicate ? `${duplicateFrom?.name}-copy` : "nightly-payments-scan"}
                    required={isDuplicate}
                  />
                </FlowField>
              </div>
            ) : null}

            <OptionRows label="Options" className="pt-1">
              <OptionRow
                icon={CalendarClock}
                title="Scheduling"
                summary={scheduleSummary(spec)}
                modified={spec.manualOnly || Boolean(spec.timeZone.trim()) || spec.concurrencyPolicy === "Allow" || spec.suspend}
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
                      disabled={spec.manualOnly}
                    />
                  }
                />
              </OptionRow>

              {spec.targetType === "repository" && (
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
              )}

              <OptionRow
                icon={Crosshair}
                title="Scope"
                summary={scopeSummary(spec)}
                modified={Boolean(
                  spec.focus.trim() ||
                    spec.includePaths.trim() ||
                    spec.excludePaths.trim() ||
                    spec.languages.trim() ||
                    spec.authorizedNetworkTargets.trim() ||
                    spec.securityProgramRef.trim(),
                )}
              >
                <FlowField
                  id="scan-security-program-ref"
                  label="Security program"
                  hint="Optional operator-verified scope snapshot. The program URL is provenance only and does not authorize network testing."
                >
                  <select
                    id="scan-security-program-ref"
                    className={selectClass}
                    value={spec.securityProgramRef}
                    onChange={(event) => update("securityProgramRef", event.target.value)}
                  >
                    <option value="">None</option>
                    {securityPrograms.map((program) => (
                      <option key={program.name} value={program.name}>
                        {program.displayName} ({program.provider})
                      </option>
                    ))}
                    {spec.securityProgramRef !== "" &&
                      !securityPrograms.some((program) => program.name === spec.securityProgramRef) && (
                        <option value={spec.securityProgramRef}>{spec.securityProgramRef}</option>
                      )}
                  </select>
                </FlowField>
                {selectedSecurityProgram && (
                  <div
                    className="space-y-1.5 rounded-md border border-border/70 bg-muted/20 p-3 text-xs"
                    data-testid="security-program-summary"
                  >
                    <p className="font-medium text-foreground">
                      {selectedSecurityProgram.displayName} · {selectedSecurityProgram.provider}
                    </p>
                    <p className="text-muted-foreground">
                      Provenance:{" "}
                      <a
                        href={selectedSecurityProgram.programUrl}
                        target="_blank"
                        rel="noreferrer"
                        className="font-mono underline underline-offset-2"
                      >
                        {selectedSecurityProgram.programUrl}
                      </a>
                    </p>
                    <p className="whitespace-pre-wrap text-muted-foreground">
                      Scope snapshot: {selectedSecurityProgram.scopePolicy}
                    </p>
                    <p className="font-medium text-foreground">
                      This provenance URL does not authorize network testing. Add every network
                      target explicitly below.
                    </p>
                  </div>
                )}
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
                <FlowField
                  id="scan-authorized-network-targets"
                  label="Authorized network targets"
                  hint="Additional comma or newline separated hosts, wildcard domains, host:port pairs, CIDRs, or http(s) URLs. The primary website and its subdomains are authorized automatically."
                >
                  <Textarea
                    id="scan-authorized-network-targets"
                    value={spec.authorizedNetworkTargets}
                    onChange={(event) => update("authorizedNetworkTargets", event.target.value)}
                    className="min-h-16 font-mono"
                    placeholder="staging.example.com, https://api.example.com, 10.0.0.0/8"
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
                icon={ShieldCheck}
                title="Policy pack & budgets"
                summary={policyPackSummary(spec)}
                modified={Boolean(spec.policyPackRef.trim()) || !budgetDraftIsZero(spec.budgets)}
              >
                <FlowField
                  id="scan-policy-pack-ref"
                  label="Policy pack"
                  hint="A SecurityPolicyPack supplies defaults, enforced floors this scan may not relax, governed suppressions, retention, and budgets. Manage packs in the security library."
                >
                  <select
                    id="scan-policy-pack-ref"
                    className={selectClass}
                    value={spec.policyPackRef}
                    onChange={(event) => update("policyPackRef", event.target.value)}
                  >
                    <option value="">None</option>
                    {policyPacks.map((pack) => (
                      <option key={pack.name} value={pack.name}>{pack.name}</option>
                    ))}
                    {spec.policyPackRef !== "" &&
                      !policyPacks.some((pack) => pack.name === spec.policyPackRef) && (
                        <option value={spec.policyPackRef}>{spec.policyPackRef}</option>
                      )}
                  </select>
                </FlowField>
                {selectedPolicyPack && (
                  <div
                    className="space-y-1.5 rounded-md border border-border/70 bg-muted/20 p-3 text-xs"
                    data-testid="policy-pack-summary"
                  >
                    <p className="font-medium text-foreground">
                      Inherited from {selectedPolicyPack.name}
                    </p>
                    <ul className="space-y-0.5 text-muted-foreground">
                      {selectedPolicyPack.minSeverity && (
                        <li>Minimum severity: {selectedPolicyPack.minSeverity}</li>
                      )}
                      {selectedPolicyPack.failOnSeverity && (
                        <li>Fail on severity: {selectedPolicyPack.failOnSeverity}</li>
                      )}
                      {selectedPolicyPack.dedupe && (
                        <li>Dedupe: {selectedPolicyPack.dedupe.enabled ? "on" : "off"}</li>
                      )}
                      {selectedPolicyPack.requiredCategories.length > 0 && (
                        <li>Required categories: {selectedPolicyPack.requiredCategories.join(", ")}</li>
                      )}
                      {selectedPolicyPack.allowedRuntimeProfiles.length > 0 && (
                        <li>Allowed runtime profiles: {selectedPolicyPack.allowedRuntimeProfiles.join(", ")}</li>
                      )}
                      {selectedPolicyPack.defaultRankerRefs.length > 0 && (
                        <li>Default rankers: {selectedPolicyPack.defaultRankerRefs.join(", ")}</li>
                      )}
                      {selectedPolicyPack.defaultPostScriptRefs.length > 0 && (
                        <li>Default post-scripts: {selectedPolicyPack.defaultPostScriptRefs.join(", ")}</li>
                      )}
                      {packBudgetSummary(selectedPolicyPack.budgets) && (
                        <li>Pack budgets: {packBudgetSummary(selectedPolicyPack.budgets)}</li>
                      )}
                      {packRetentionSummary(selectedPolicyPack.retention) && (
                        <li>Retention: {packRetentionSummary(selectedPolicyPack.retention)}</li>
                      )}
                      {selectedPolicyPack.suppressions.length > 0 && (
                        <li>
                          Governed suppressions: {selectedPolicyPack.suppressions.length} rule
                          {selectedPolicyPack.suppressions.length === 1 ? "" : "s"}
                        </li>
                      )}
                    </ul>
                    {selectedPolicyPack.enforced.length > 0 && (
                      <p data-testid="policy-pack-enforced">
                        <span className="font-medium text-foreground">Enforced — this scan may not relax:</span>{" "}
                        {selectedPolicyPack.enforced
                          .map((field) => ENFORCED_LABELS[field] ?? field)
                          .join(", ")}
                      </p>
                    )}
                  </div>
                )}
                <FlowField
                  id="scan-budgets"
                  label="Per-scan budgets"
                  hint="Caps for each run of this scan; empty or 0 = unlimited (or the pack default). Enforced entirely platform-side."
                >
                  <div id="scan-budgets" className="pt-1">
                    <BudgetFields idPrefix="scan-budget" value={spec.budgets} onChange={(budgets) => update("budgets", budgets)} />
                  </div>
                </FlowField>
                {packEnforcesBudgets && (
                  <p
                    role="note"
                    data-testid="policy-pack-budget-warning"
                    className="rounded-md border border-amber-500/40 bg-amber-500/10 p-2.5 text-xs"
                  >
                    The policy pack <span className="font-mono">{selectedPolicyPack?.name}</span>{" "}
                    enforces budgets: this scan may tighten a pack limit but not raise or remove
                    one. A budget above the pack's limit is rejected when the scan is saved or run.
                  </p>
                )}
              </OptionRow>

              {spec.targetType === "repository" && (
                <OptionRow
                  icon={GitPullRequest}
                  title="Repository events"
                summary={repositoryEventsSummary(spec)}
                modified={spec.onPullRequest || spec.onPush || spec.checksEnabled}
              >
                <FlowField
                  id="scan-trigger-repository-ref"
                  label="GitHub repository connection"
                  hint="Name of a GitHubRepository resource in this namespace. Its webhook deliveries trigger the scan and its credentials publish checks and read diffs — the scan run itself never receives them."
                >
                  <Input
                    id="scan-trigger-repository-ref"
                    value={spec.triggersRepositoryRef}
                    onChange={(event) => update("triggersRepositoryRef", event.target.value)}
                    placeholder="my-repo-connection"
                  />
                </FlowField>
                <FlowSwitchRow
                  id="scan-on-pull-request"
                  label="Scan pull requests"
                  hint="Run a scan pinned to the PR head commit when a PR is opened, reopened, or updated."
                  control={
                    <Switch
                      id="scan-on-pull-request"
                      checked={spec.onPullRequest}
                      onCheckedChange={(checked) => update("onPullRequest", checked)}
                    />
                  }
                />
                <FlowSwitchRow
                  id="scan-on-push"
                  label="Scan pushes"
                  hint="Run a scan pinned to the pushed head commit."
                  control={
                    <Switch
                      id="scan-on-push"
                      checked={spec.onPush}
                      onCheckedChange={(checked) => update("onPush", checked)}
                    />
                  }
                />
                {spec.onPush && (
                  <FlowField
                    id="scan-trigger-branches"
                    label="Push branch filters"
                    hint="Comma-separated globs, e.g. main, release/*. Empty = every branch."
                  >
                    <Input
                      id="scan-trigger-branches"
                      value={spec.triggerBranches}
                      onChange={(event) => update("triggerBranches", event.target.value)}
                      placeholder="main, release/*"
                      className="font-mono"
                    />
                  </FlowField>
                )}
                <FlowSwitchRow
                  id="scan-diff-scope"
                  label="Diff scope"
                  hint="Focus event-triggered scans on the files changed since the merge base (PRs) or the push range. Falls back to a full scan — stated in the run prompt and scan status — when the diff cannot be computed."
                  control={
                    <Switch
                      id="scan-diff-scope"
                      checked={spec.diffScope}
                      onCheckedChange={(checked) => update("diffScope", checked)}
                    />
                  }
                />
                <FlowSwitchRow
                  id="scan-allow-forks"
                  label="Allow fork pull requests"
                  hint="Off (recommended): PRs from forks are skipped with a visible condition. On: fork PRs are scanned, but the run's GitHub credential is stripped so untrusted contributions can never reach a write token."
                  control={
                    <Switch
                      id="scan-allow-forks"
                      checked={spec.allowForks}
                      onCheckedChange={(checked) => update("allowForks", checked)}
                    />
                  }
                />
                <FlowSwitchRow
                  id="scan-checks-enabled"
                  label="Publish GitHub checks"
                  hint="After each scan of a specific commit, publish a check with the pass/fail conclusion from the fail-on severity policy. The default summary contains only severity counts and a dashboard link."
                  control={
                    <Switch
                      id="scan-checks-enabled"
                      checked={spec.checksEnabled}
                      onCheckedChange={(checked) => update("checksEnabled", checked)}
                    />
                  }
                />
                {spec.checksEnabled && (
                  <>
                    <FlowSwitchRow
                      id="scan-include-finding-summaries"
                      label="Include finding titles in the check"
                      hint="Opt-in: adds finding titles and file locations to the check summary. Evidence and proof-of-concept content is never published either way."
                      control={
                        <Switch
                          id="scan-include-finding-summaries"
                          checked={spec.includeFindingSummaries}
                          onCheckedChange={(checked) => update("includeFindingSummaries", checked)}
                        />
                      }
                    />
                    <FlowSwitchRow
                      id="scan-upload-sarif"
                      label="Upload SARIF to code scanning"
                      hint="Opt-in: upload the scan's SARIF report to GitHub code scanning for the scanned commit."
                      control={
                        <Switch
                          id="scan-upload-sarif"
                          checked={spec.uploadSarif}
                          onCheckedChange={(checked) => update("uploadSarif", checked)}
                        />
                      }
                    />
                  </>
                )}
                </OptionRow>
              )}

              <OptionRow
                icon={Bell}
                title="Notifications"
                summary={notificationsSummary(notifications)}
                modified={notifications.length > 0}
              >
                <p className="text-xs text-muted-foreground">
                  Rules notify about new or regressed findings after each successful run. Each finding
                  notifies at most once per rule and channel — the sent marker is persisted, so retries
                  and re-runs never repeat a notification. Messages carry severity, title, location, and
                  a dashboard link, never evidence.
                </p>
                {notifications.map((rule, index) => (
                  <div key={index} className="space-y-3 rounded-md border p-3">
                    <div className="grid gap-4 sm:grid-cols-2">
                      <FlowField id={`scan-notify-name-${index}`} label="Rule name" required>
                        <Input
                          id={`scan-notify-name-${index}`}
                          value={rule.name}
                          onChange={(event) => updateNotification(index, { name: event.target.value })}
                          placeholder="critical-alerts"
                        />
                      </FlowField>
                      <FlowField id={`scan-notify-min-severity-${index}`} label="Minimum severity">
                        <select
                          id={`scan-notify-min-severity-${index}`}
                          className={selectClass}
                          value={rule.minSeverity}
                          onChange={(event) => updateNotification(index, { minSeverity: event.target.value })}
                        >
                          <option value="">high (default)</option>
                          {SEVERITY_OPTIONS.map((s) => (
                            <option key={s} value={s}>{s}</option>
                          ))}
                        </select>
                      </FlowField>
                    </div>
                    <FlowField id={`scan-notify-on-${index}`} label="Notify on">
                      <select
                        id={`scan-notify-on-${index}`}
                        className={selectClass}
                        value={rule.notifyOn}
                        onChange={(event) => updateNotification(index, { notifyOn: event.target.value })}
                      >
                        <option value="new-and-regressed">new and regressed findings</option>
                        <option value="new">new findings only</option>
                        <option value="regressed">regressed findings only</option>
                      </select>
                    </FlowField>
                    <FlowField
                      id={`scan-notify-slack-${index}`}
                      label="Slack webhook secret"
                      hint='Secret in this namespace with the incoming-webhook URL under key "url". Empty = no Slack message.'
                    >
                      <Input
                        id={`scan-notify-slack-${index}`}
                        value={rule.slackWebhookSecretRef}
                        onChange={(event) => updateNotification(index, { slackWebhookSecretRef: event.target.value })}
                        placeholder="slack-security-webhook"
                      />
                    </FlowField>
                    <FlowSwitchRow
                      id={`scan-notify-github-${index}`}
                      label="Create GitHub issues"
                      hint="One issue per finding via the repository connection's credentials. Bodies contain metadata and a dashboard link, never evidence."
                      control={
                        <Switch
                          id={`scan-notify-github-${index}`}
                          checked={rule.githubIssues}
                          onCheckedChange={(checked) => updateNotification(index, { githubIssues: checked })}
                        />
                      }
                    />
                    {rule.githubIssues && (
                      <FlowField
                        id={`scan-notify-github-repo-${index}`}
                        label="GitHub repository connection override"
                        hint="Empty = use the Repository events connection."
                      >
                        <Input
                          id={`scan-notify-github-repo-${index}`}
                          value={rule.githubRepositoryRef}
                          onChange={(event) => updateNotification(index, { githubRepositoryRef: event.target.value })}
                          placeholder="security-tracker-repo"
                        />
                      </FlowField>
                    )}
                    <div className="grid gap-4 sm:grid-cols-2">
                      <FlowField
                        id={`scan-notify-linear-key-${index}`}
                        label="Linear API key secret"
                        hint='Secret with the key under "api-key". Set together with the team ID.'
                      >
                        <Input
                          id={`scan-notify-linear-key-${index}`}
                          value={rule.linearApiKeySecretRef}
                          onChange={(event) => updateNotification(index, { linearApiKeySecretRef: event.target.value })}
                          placeholder="linear-api-key"
                        />
                      </FlowField>
                      <FlowField id={`scan-notify-linear-team-${index}`} label="Linear team ID">
                        <Input
                          id={`scan-notify-linear-team-${index}`}
                          value={rule.linearTeamId}
                          onChange={(event) => updateNotification(index, { linearTeamId: event.target.value })}
                          placeholder="TEAM-uuid"
                        />
                      </FlowField>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => setNotifications((prev) => prev.filter((_, i) => i !== index))}
                    >
                      Remove rule
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setNotifications((prev) => [
                      ...prev,
                      {
                        name: "",
                        minSeverity: "",
                        notifyOn: "new-and-regressed",
                        slackWebhookSecretRef: "",
                        githubIssues: false,
                        githubRepositoryRef: "",
                        linearApiKeySecretRef: "",
                        linearTeamId: "",
                      },
                    ])
                  }
                >
                  Add notification rule
                </Button>
              </OptionRow>

              <OptionRow
                icon={ListChecks}
                title="Workflow tasks"
                summary={
                  spec.workflowRef
                    ? `Library workflow: ${spec.workflowRef}`
                    : tasks.length
                      ? `${tasks.length} custom task${tasks.length === 1 ? "" : "s"}`
                      : "Default workflow"
                }
                modified={tasks.length > 0 || Boolean(spec.workflowRef)}
              >
                <FlowField
                  id="scan-workflow-ref"
                  label="Library workflow"
                  hint="Referenced content is resolved and snapshotted when each run starts, so later library edits never change runs that already happened. Mutually exclusive with inline tasks."
                >
                  <select
                    id="scan-workflow-ref"
                    className={selectClass}
                    value={spec.workflowRef}
                    onChange={(event) => update("workflowRef", event.target.value)}
                  >
                    <option value="">None — edit tasks inline</option>
                    {libraryWorkflows.map((workflow) => (
                      <option key={workflow.name} value={workflow.name}>
                        {workflow.name} ({workflow.tasks.length} task{workflow.tasks.length === 1 ? "" : "s"})
                      </option>
                    ))}
                    {spec.workflowRef !== "" &&
                      !libraryWorkflows.some((workflow) => workflow.name === spec.workflowRef) && (
                        <option value={spec.workflowRef}>{spec.workflowRef}</option>
                      )}
                  </select>
                </FlowField>
                {spec.workflowRef !== "" && (
                  <p className="text-xs text-muted-foreground">
                    This scan runs the library workflow{" "}
                    <span className="font-mono">{spec.workflowRef}</span>. Inline tasks are
                    disabled while a library workflow is selected.
                  </p>
                )}
                {spec.workflowRef === "" && (
                <>
                <p className="text-xs text-muted-foreground">
                  Leave empty to deterministically infer blockchain routing from repository URLs and
                  scope focus, include paths, and languages. Recognized evidence selects
                  smart-contract-review, blockchain-protocol-audit, or cosmos-abci-halt-review;
                  otherwise the scan uses default-deep-scan. Custom tasks replace automatic routing
                  entirely; depends_on must reference other task names.
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
                      { name: "", objective: "", category: "", role: "", model: "", dependsOn: "" },
                    ])
                  }
                >
                  Add workflow task
                </Button>
                </>
                )}
              </OptionRow>

              <OptionRow
                icon={Route}
                title="Execution"
                summary={executionSummary(spec)}
                modified={
                  Boolean(spec.executionMode.trim() || spec.taskMaxRetries.trim() || spec.retryBackoff.trim()) ||
                  spec.parameterValues.length > 0
                }
              >
                <FlowField
                  id="scan-execution-mode"
                  label="Execution mode"
                  hint="Coordinator seeds one orchestrating run that delegates to in-process sub-agents. Deterministic compiles the workflow into controller-scheduled per-task runs with enforced dependencies, retries, and budgets."
                >
                  <select
                    id="scan-execution-mode"
                    className={selectClass}
                    value={spec.executionMode}
                    onChange={(event) => update("executionMode", event.target.value)}
                  >
                    <option value="">deterministic (default)</option>
                    <option value="coordinator">coordinator</option>
                  </select>
                </FlowField>
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="scan-task-max-retries"
                    label="Task max retries"
                    hint="Default per-task retry budget (0-10) in deterministic mode; empty = 1."
                  >
                    <Input
                      id="scan-task-max-retries"
                      type="number"
                      min={0}
                      max={10}
                      value={spec.taskMaxRetries}
                      onChange={(event) => update("taskMaxRetries", event.target.value)}
                      placeholder="1"
                    />
                  </FlowField>
                  <FlowField
                    id="scan-retry-backoff"
                    label="Retry backoff"
                    hint='Base delay before a failed task attempt is rescheduled, e.g. "30s"; empty = 30s.'
                  >
                    <Input
                      id="scan-retry-backoff"
                      value={spec.retryBackoff}
                      onChange={(event) => update("retryBackoff", event.target.value)}
                      placeholder="30s"
                      className="font-mono"
                    />
                  </FlowField>
                </div>
                <FlowField
                  id="scan-parameter-values"
                  label="Parameter values"
                  hint="Substituted for {{params.name}} references in task objectives; accepted names come from the referenced workflow's parameters."
                >
                  <div id="scan-parameter-values" className="space-y-2 pt-1">
                    {spec.parameterValues.map((entry, index) => (
                      <div key={index} className="flex items-center gap-2">
                        <Input
                          aria-label={`Parameter ${index + 1} name`}
                          value={entry.key}
                          onChange={(event) =>
                            update(
                              "parameterValues",
                              spec.parameterValues.map((e, i) =>
                                i === index ? { ...e, key: event.target.value } : e,
                              ),
                            )
                          }
                          placeholder="target_service"
                          className="font-mono"
                        />
                        <Input
                          aria-label={`Parameter ${index + 1} value`}
                          value={entry.value}
                          onChange={(event) =>
                            update(
                              "parameterValues",
                              spec.parameterValues.map((e, i) =>
                                i === index ? { ...e, value: event.target.value } : e,
                              ),
                            )
                          }
                          placeholder="payments-api"
                        />
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          aria-label={`Remove parameter ${index + 1}`}
                          onClick={() =>
                            update(
                              "parameterValues",
                              spec.parameterValues.filter((_, i) => i !== index),
                            )
                          }
                        >
                          Remove
                        </Button>
                      </div>
                    ))}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() =>
                        update("parameterValues", [...spec.parameterValues, { key: "", value: "" }])
                      }
                    >
                      Add parameter value
                    </Button>
                  </div>
                </FlowField>
              </OptionRow>

              <OptionRow
                icon={SlidersHorizontal}
                title="Rankers & post-scripts"
                summary={
                  rankers.length || postScripts.length || spec.rankerRefs.length || spec.postScriptRefs.length
                    ? `${rankers.length + spec.rankerRefs.length} ranker${rankers.length + spec.rankerRefs.length === 1 ? "" : "s"} · ${postScripts.length + spec.postScriptRefs.length} post-script${postScripts.length + spec.postScriptRefs.length === 1 ? "" : "s"}`
                    : "None"
                }
                modified={
                  rankers.length > 0 ||
                  postScripts.length > 0 ||
                  spec.rankerRefs.length > 0 ||
                  spec.postScriptRefs.length > 0
                }
              >
                <p className="text-xs text-muted-foreground">
                  Severity rankers add your ranking rules to the scan prompt; post-scripts run a
                  prompt against each matching finding after the scan.
                </p>
                {(libraryRankers.length > 0 || spec.rankerRefs.length > 0) && (
                  <FlowField
                    id="scan-ranker-refs"
                    label="Library rankers"
                    hint="Appended after the inline rankers below and snapshotted when each run starts."
                  >
                    <div className="flex flex-wrap gap-2" id="scan-ranker-refs">
                      {[...new Set([...libraryRankers.map((r) => r.name), ...spec.rankerRefs])].map((name) => {
                        const checked = spec.rankerRefs.includes(name);
                        return (
                          <label key={name} className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border px-2 py-1 text-xs">
                            <input
                              type="checkbox"
                              checked={checked}
                              onChange={() =>
                                update(
                                  "rankerRefs",
                                  checked
                                    ? spec.rankerRefs.filter((r) => r !== name)
                                    : [...spec.rankerRefs, name],
                                )
                              }
                            />
                            <span className="font-mono">{name}</span>
                          </label>
                        );
                      })}
                    </div>
                  </FlowField>
                )}
                {(libraryPostScripts.length > 0 || spec.postScriptRefs.length > 0) && (
                  <FlowField
                    id="scan-post-script-refs"
                    label="Library post-scripts"
                    hint="Appended after the inline post-scripts below and snapshotted when each run starts."
                  >
                    <div className="flex flex-wrap gap-2" id="scan-post-script-refs">
                      {[...new Set([...libraryPostScripts.map((p) => p.name), ...spec.postScriptRefs])].map((name) => {
                        const checked = spec.postScriptRefs.includes(name);
                        return (
                          <label key={name} className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border px-2 py-1 text-xs">
                            <input
                              type="checkbox"
                              checked={checked}
                              onChange={() =>
                                update(
                                  "postScriptRefs",
                                  checked
                                    ? spec.postScriptRefs.filter((r) => r !== name)
                                    : [...spec.postScriptRefs, name],
                                )
                              }
                            />
                            <span className="font-mono">{name}</span>
                          </label>
                        );
                      })}
                    </div>
                  </FlowField>
                )}
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
                          <option value="high-and-above-actionable">high and above, while actionable</option>
                          <option value="medium-and-above-actionable">medium and above, while actionable</option>
                        </select>
                      </FlowField>
                      {(script.runOn === "high-and-above-actionable" ||
                        script.runOn === "medium-and-above-actionable") && (
                        <p className="text-xs text-muted-foreground md:col-span-2">
                          Skips this stage before its first attempt when a successful earlier stage has already marked
                          the finding false positive, accepted risk, or fixed. Use “all findings” for final reporting,
                          audit, or cleanup stages.
                        </p>
                      )}
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
            <DialogClose
              render={<Button type="button" variant="ghost" size="sm" disabled={submitting} />}
            >
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
