// Fake gratefulagents backend for self-dev mode.
//
// A connect-node HTTP server that serves the real `platform.v1.PlatformService`
// and `auth.v1.AuthService` contracts (plus runtime `/api/*` metadata) from an
// in-memory Scenario. The methods the UI exercises get real fixture-backed
// implementations — including light mutation semantics so interactive `serve`
// sessions feel alive — and every other method falls back to an empty
// response, so no page ever hard-errors.
//
// Watch streams yield the current snapshot, then stay open until the client
// disconnects (matching the list+watch pattern in hooks/useWatchedList.ts).

import * as http from "node:http";
import { create } from "@bufbuild/protobuf";
import type { DescMethod, DescService } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, type ConnectRouter, type HandlerContext } from "@connectrpc/connect";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import {
  AgentRunEventSchema,
  AgentRunSchema,
  AgentRunUsageResponseSchema,
  ChatMessageSchema,
  CronEventSchema,
  ExportAgentRunArchiveResponseSchema,
  GetActivityLogResponseSchema,
  GetAgentRunErrorsResponseSchema,
  GetAgentRunLogsResponseSchema,
  GetAgentRunPullRequestsResponseSchema,
  GetAgentTraceResponseSchema,
  GetDiffResponseSchema,
  GetPresenceResponseSchema,
  GitHubAppConfigSchema,
  GitHubRepositoryEventSchema,
  LinearProjectEventSchema,
  ListAgentRunsResponseSchema,
  ListAvailableModelsResponseSchema,
  ListAvailableModesResponseSchema,
  ListBugReportsResponseSchema,
  ListCronsResponseSchema,
  ListGitHubRepositoriesResponseSchema,
  ListLinearProjectsResponseSchema,
  ListMaintainerWorkItemsResponseSchema,
  ListNotificationsResponseSchema,
  ListProjectsResponseSchema,
  ListRepositoriesResponseSchema,
  ListRuntimeImagesResponseSchema,
  GetSecurityConfigPosturesResponseSchema,
  GetSecurityFindingResponseSchema,
  GetSecurityFindingSummaryResponseSchema,
  GetSecurityOverviewResponseSchema,
  GetSecurityScanReportResponseSchema,
  ListSecurityFindingEventsResponseSchema,
  ListSecurityFindingsResponseSchema,
  ListSecurityPolicyPacksResponseSchema,
  ListSecurityPostScriptsResponseSchema,
  ListSecurityProgramsResponseSchema,
  ListSecurityRankersResponseSchema,
  ListSecuritySavedFiltersResponseSchema,
  ListSecurityScanConfigsResponseSchema,
  ListSecurityScansResponseSchema,
  ListSecurityWorkflowsResponseSchema,
  ListSharesResponseSchema,
  ListSharedWithMeResponseSchema,
  ListSkillsResponseSchema,
  ListSlackAgentsResponseSchema,
  ListSlackDraftsResponseSchema,
  ListSlackWorkspacesResponseSchema,
  ListWorkspaceFilesResponseSchema,
  ModelDefaultsSchema,
  ObservabilityBreakdownSchema,
  ObservabilityBucketSchema,
  ObservabilityDataCompletenessSchema,
  ObservabilityOverviewResponseSchema,
  ObservabilityTotalsSchema,
  PlatformService,
  ProjectEventSchema,
  ProviderOAuthResultSchema,
  ProviderOAuthStartSchema,
  ReadFileResponseSchema,
  SecurityFindingEventSchema,
  SecuritySkillsStatusSchema,
  SkillInfoSchema,
  SwitchAgentRunModeResponseSchema,
  type AgentRun,
  type SecurityFinding,
  type SecurityScan,
  type UpsertSkillRequest,
} from "../../../frontend/src/rpc/platform/service_pb";
import {
  AuthService,
  LoginResponseSchema,
  LogoutResponseSchema,
  RefreshTokenResponseSchema,
  SearchUsersResponseSchema,
  UserSchema,
} from "../../../frontend/src/rpc/auth/service_pb";
import type { Scenario } from "../scenario";
import { runKey } from "../scenario";
import { unix } from "../time";

export interface FakeBackend {
  port: number;
  url: string;
  scenario: Scenario;
  close(): Promise<void>;
}

const ACCESS_TOKEN = "selfdev-access-token";
const REFRESH_TOKEN = "selfdev-refresh-token";

/** Resolves when the handler's client disconnects. Keeps watch streams open. */
function clientGone(ctx: HandlerContext): Promise<void> {
  return new Promise((resolve) => {
    if (ctx.signal.aborted) return resolve();
    ctx.signal.addEventListener("abort", () => resolve(), { once: true });
  });
}

function notFound(what: string): ConnectError {
  return new ConnectError(`${what} not found`, Code.NotFound);
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyImpl = Record<string, any>;

/**
 * Fills every unimplemented method of `service` with a benign default:
 * unary → empty response message, server-streaming → open-but-silent stream.
 */
function withDefaults(service: DescService, impl: AnyImpl): AnyImpl {
  const out: AnyImpl = { ...impl };
  for (const method of service.methods as DescMethod[]) {
    if (out[method.localName]) continue;
    if (method.methodKind === "unary") {
      out[method.localName] = async () => create(method.output, {});
    } else if (method.methodKind === "server_streaming") {
      out[method.localName] = async function* (_req: unknown, ctx: HandlerContext) {
        await clientGone(ctx);
        // Unreachable yield keeps this a generator without emitting anything.
        if (false as boolean) yield create(method.output, {});
      };
    }
  }
  return out;
}

// Converts the integer counters of a synthetic observability totals record to
// the bigint fields ObservabilityTotals expects (cost fields stay numbers).
function bigintTotals(v: {
  runs: number; inputTokens: number; outputTokens: number; toolCalls: number; toolErrors: number;
  subagents: number; subagentFailures: number; llmAttempts: number; llmFailures: number;
  compactions: number; tokensReclaimed: number; generationInputTokens: number; generationOutputTokens: number;
}) {
  return {
    runs: BigInt(v.runs),
    inputTokens: BigInt(v.inputTokens),
    outputTokens: BigInt(v.outputTokens),
    toolCalls: BigInt(v.toolCalls),
    toolErrors: BigInt(v.toolErrors),
    subagents: BigInt(v.subagents),
    subagentFailures: BigInt(v.subagentFailures),
    llmAttempts: BigInt(v.llmAttempts),
    llmFailures: BigInt(v.llmFailures),
    compactions: BigInt(v.compactions),
    tokensReclaimed: BigInt(v.tokensReclaimed),
    generationInputTokens: BigInt(v.generationInputTokens),
    generationOutputTokens: BigInt(v.generationOutputTokens),
  };
}

// ---------------------------------------------------------------------------
// Security aggregation: the finding list, summary, overview, and posture
// endpoints all derive from the scenario's finding rows the way the Postgres
// store does, so filters and counters agree with each other.
// ---------------------------------------------------------------------------

const ACTIONABLE_FINDING_STATUSES = new Set(["open", "triaged", "confirmed"]);
const SEVERITIES = new Set(["critical", "high", "medium", "low", "info"]);

function isSuppressed(f: SecurityFinding): boolean {
  return f.suppressedBy !== "";
}

/** Mirrors the Postgres summary: fixed keys are always present, even at zero. */
function findingCounts(findings: SecurityFinding[], includeSuppressed: boolean): Record<string, number> {
  const counts: Record<string, number> = {
    total: 0, open: 0, actionable: 0, suppressed: 0,
    source_agent: 0, source_scanner: 0, correlated: 0,
    baseline_new: 0, baseline_recurring: 0, baseline_regressed: 0,
    baseline_resolved: 0, baseline_reopened: 0, baseline_tracked: 0,
  };
  for (const severity of SEVERITIES) {
    counts[`open_${severity}`] = 0;
    counts[`actionable_${severity}`] = 0;
  }
  const bump = (key: string) => {
    counts[key] = (counts[key] ?? 0) + 1;
  };
  for (const f of findings) {
    // Postgres summarises deduplicated rows only (SummarizeSecurityFindingsScoped
    // filters `duplicate_of IS NULL`), so counting duplicates here would make the
    // fake backend disagree with production and hand the UI impossible totals.
    if (f.duplicateOf) continue;
    if (isSuppressed(f)) {
      bump("suppressed");
      if (!includeSuppressed) continue;
    }
    bump("total");
    bump(f.severity);
    if (f.status === "open") {
      bump("open");
      bump(`open_${f.severity}`);
    }
    if (ACTIONABLE_FINDING_STATUSES.has(f.status)) {
      bump("actionable");
      bump(`actionable_${f.severity}`);
    }
    if (f.baselineState) {
      bump("baseline_tracked");
      bump(`baseline_${f.baselineState}`);
    }
    bump(f.sourceKind === "scanner" ? "source_scanner" : "source_agent");
    if (f.correlatedFingerprints.length > 0) bump("correlated");
  }
  return counts;
}

function secondsBetween(from: Timestamp | undefined, to: Timestamp | undefined): number | null {
  if (!from || !to) return null;
  return (timestampDate(to).getTime() - timestampDate(from).getTime()) / 1000;
}

function median(values: number[]): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 1 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

function mean(values: number[]): number {
  if (values.length === 0) return 0;
  return values.reduce((sum, v) => sum + v, 0) / values.length;
}

function findingTrends(findings: SecurityFinding[]) {
  const toTriage: number[] = [];
  const toResolution: number[] = [];
  for (const f of findings) {
    const triage = secondsBetween(f.firstSeenAt, f.triagedAt);
    if (triage !== null) toTriage.push(triage);
    const resolution = secondsBetween(f.firstSeenAt, f.resolvedAt);
    if (resolution !== null) toResolution.push(resolution);
  }
  return {
    triagedCount: toTriage.length,
    resolvedCount: toResolution.length,
    avgTimeToTriageSeconds: mean(toTriage),
    medianTimeToTriageSeconds: median(toTriage),
    avgTimeToResolutionSeconds: mean(toResolution),
    medianTimeToResolutionSeconds: median(toResolution),
  };
}

/**
 * Synthesizes a finding's audit trail from the state recorded on the row, so
 * the history panel and the finding detail timeline stay consistent with the
 * finding they describe. Newest first, matching the store's ordering.
 */
function findingEvents(f: SecurityFinding, actor: string) {
  const raw: { at: Timestamp | undefined; eventType: string; note: string; detail: string }[] = [
    {
      at: f.firstSeenAt,
      eventType: "detected",
      note: `Reported by ${f.sourceAgent || f.tool || "the scan"}`,
      detail: JSON.stringify({ severity: f.severity, scanRun: f.runName }),
    },
  ];
  if (f.triagedAt) {
    raw.push({
      at: f.triagedAt,
      eventType: "status_changed",
      note: `Status set to ${f.status}`,
      detail: JSON.stringify({ from: "open", to: f.status }),
    });
  }
  if (f.assignee) {
    raw.push({
      at: f.triagedAt ?? f.firstSeenAt,
      eventType: "assigned",
      note: `Assigned to ${f.assignee}`,
      detail: JSON.stringify({ assignee: f.assignee }),
    });
  }
  if (f.ticketUrl) {
    raw.push({
      at: f.triagedAt ?? f.firstSeenAt,
      eventType: "ticket_linked",
      note: `Linked ${f.ticketProvider || "external"} ticket`,
      detail: JSON.stringify({ ticketUrl: f.ticketUrl }),
    });
  }
  if (f.suppressedAt) {
    raw.push({
      at: f.suppressedAt,
      eventType: "suppressed",
      note: `Suppressed by ${f.suppressedBy}: ${f.suppressedReason}`,
      detail: JSON.stringify({ rule: f.suppressedBy, owner: f.suppressedOwner }),
    });
  }
  if (f.resolvedAt) {
    raw.push({
      at: f.resolvedAt,
      eventType: "resolved",
      note: "Resolved by a later scan run",
      detail: JSON.stringify({ scanRun: f.runName }),
    });
  }
  const ordered = raw
    .map((e, index) => ({ ...e, index }))
    .sort((a, b) => {
      const at = a.at ? timestampDate(a.at).getTime() : 0;
      const bt = b.at ? timestampDate(b.at).getTime() : 0;
      return at - bt || a.index - b.index;
    });
  return ordered
    .map((e, i) =>
      create(SecurityFindingEventSchema, {
        id: BigInt(i + 1),
        eventType: e.eventType,
        actor: e.eventType === "detected" ? f.sourceAgent || f.tool || "scanner" : actor,
        note: e.note,
        detail: e.detail,
        createdAt: e.at,
      }),
    )
    .reverse();
}

function scanReportMarkdown(scan: SecurityScan, findings: SecurityFinding[]): string {
  const lines = [
    `# Security scan report — ${scan.runName}`,
    "",
    `- Configuration: ${scan.scanName}`,
    `- Repository: ${scan.repository} @ ${scan.revision}`,
    `- Status: ${scan.status}`,
    `- Findings: ${findings.length}`,
    "",
    "## Findings",
    "",
  ];
  for (const f of findings) {
    lines.push(
      `### [${f.severity}] ${f.title}`,
      "",
      `- Category: ${f.category}`,
      `- Location: ${f.filePath}:${f.startLine}`,
      `- Status: ${f.status}`,
      "",
      f.description,
      "",
    );
  }
  return lines.join("\n");
}

function scanReportSarif(scan: SecurityScan, findings: SecurityFinding[]): string {
  return JSON.stringify(
    {
      version: "2.1.0",
      $schema: "https://json.schemastore.org/sarif-2.1.0.json",
      runs: [
        {
          tool: { driver: { name: "gratefulagents-security", informationUri: "https://gratefulagents.dev" } },
          versionControlProvenance: [{ repositoryUri: scan.repository, revisionId: scan.revision }],
          results: findings.map((f) => ({
            ruleId: f.fingerprint,
            level: f.severity === "critical" || f.severity === "high" ? "error" : "warning",
            message: { text: f.title },
            locations: [
              {
                physicalLocation: {
                  artifactLocation: { uri: f.filePath },
                  region: { startLine: f.startLine || 1 },
                },
              },
            ],
          })),
        },
      ],
    },
    null,
    2,
  );
}

function buildPlatformImpl(s: Scenario): AnyImpl {
  const findRun = (namespace: string, name: string): AgentRun | undefined =>
    s.runs.find((r) => r.namespace === namespace && r.name === name);

  const mustRun = (namespace: string, name: string): AgentRun => {
    const run = findRun(namespace, name);
    if (!run) throw notFound(`agent run ${namespace}/${name}`);
    return run;
  };

  const impl: AnyImpl = {
    // ---- Agent runs -------------------------------------------------------
    listAgentRuns: async (req: { namespace: string; sharedWithMe: boolean }) =>
      create(ListAgentRunsResponseSchema, {
        runs: s.runs.filter((r) => {
          if (req.namespace && r.namespace !== req.namespace) return false;
          if (req.sharedWithMe) return r.owner?.userId !== s.user.id;
          return true;
        }),
      }),
    getAgentRun: async (req: { namespace: string; name: string }) => mustRun(req.namespace, req.name),
    watchAgentRuns: async function* (req: { namespace: string }, ctx: HandlerContext) {
      for (const run of s.runs) {
        if (req.namespace && run.namespace !== req.namespace) continue;
        yield create(AgentRunEventSchema, { type: "ADDED", run });
      }
      await clientGone(ctx);
    },
    watchAgentRun: async function* (req: { namespace: string; name: string }, ctx: HandlerContext) {
      yield mustRun(req.namespace, req.name);
      await clientGone(ctx);
    },
    getActivityLog: async (req: { namespace: string; name: string }) =>
      s.activityLogs[runKey(req.namespace, req.name)] ?? create(GetActivityLogResponseSchema, { isComplete: true }),
    watchActivityLog: async function* (req: { namespace: string; name: string }, ctx: HandlerContext) {
      const log = s.activityLogs[runKey(req.namespace, req.name)];
      if (log) yield log;
      await clientGone(ctx);
    },
    getAgentRunUsage: async (req: { namespace: string; name: string }) =>
      s.usage[runKey(req.namespace, req.name)] ??
      create(AgentRunUsageResponseSchema, { isAvailable: false, isComplete: true }),
    getAgentRunPullRequests: async (req: { namespace: string; name: string }) =>
      create(GetAgentRunPullRequestsResponseSchema, {
        pullRequests: s.pullRequests[runKey(req.namespace, req.name)] ?? [],
      }),
    getDiff: async (req: { namespace: string; name: string }) =>
      create(GetDiffResponseSchema, {
        diff: s.diffs[runKey(req.namespace, req.name)] ?? "",
        isComplete: false,
        source: s.diffs[runKey(req.namespace, req.name)] ? "pod" : "unavailable",
      }),
    watchDiff: async function* (req: { namespace: string; name: string }, ctx: HandlerContext) {
      yield create(GetDiffResponseSchema, {
        diff: s.diffs[runKey(req.namespace, req.name)] ?? "",
        isComplete: false,
        source: s.diffs[runKey(req.namespace, req.name)] ? "pod" : "unavailable",
      });
      await clientGone(ctx);
    },
    getAgentTrace: async (req: { namespace: string; name: string }) =>
      s.traces[runKey(req.namespace, req.name)] ?? create(GetAgentTraceResponseSchema, { isComplete: true }),
    getAgentRunErrors: async () => create(GetAgentRunErrorsResponseSchema, { isComplete: true }),
    getAgentRunLogs: async (req: { namespace: string; name: string }) =>
      create(GetAgentRunLogsResponseSchema, {
        content: [
          "2026-06-18T10:14:01.021Z INFO starting worker session",
          `2026-06-18T10:14:02.117Z INFO loading AgentRun ${req.namespace}/${req.name}`,
          "2026-06-18T10:14:04.804Z INFO repositories ready",
          "2026-06-18T10:14:05.392Z INFO agent runtime connected",
        ].join("\n") + "\n",
        podName: `${req.name}-worker`,
        available: true,
        isComplete: false,
      }),
    watchAgentTrace: async function* (req: { namespace: string; name: string }, ctx: HandlerContext) {
      const trace = s.traces[runKey(req.namespace, req.name)];
      if (trace) yield trace;
      await clientGone(ctx);
    },
    sendAgentRunMessage: async (req: { namespace: string; name: string; message: string }) => {
      const run = mustRun(req.namespace, req.name);
      run.conversation.push(
        create(ChatMessageSchema, {
          role: "user",
          content: req.message,
          timestampUnix: unix(new Date()),
          deliveredAtUnix: unix(new Date()),
        }),
      );
      return {};
    },
    createAgentRun: async (req: {
      namespace: string;
      name: string;
      repoUrl: string;
      baseBranch: string;
      model: string;
      userRequest: string;
    }) => {
      const name = req.name || `run-created-${s.runs.length + 1}`;
      const run = create(AgentRunSchema, {
        namespace: req.namespace || s.namespace,
        name,
        displayName: req.userRequest.slice(0, 48) || name,
        repoUrl: req.repoUrl,
        baseBranch: req.baseBranch || "main",
        model: req.model,
        workflowMode: "chat",
        phase: "Pending",
        queueState: "Queued",
        createdAtUnix: unix(new Date()),
        conversation: req.userRequest
          ? [create(ChatMessageSchema, { role: "user", content: req.userRequest, timestampUnix: unix(new Date()) })]
          : [],
      });
      s.runs.unshift(run);
      return run;
    },
    cancelAgentRun: async (req: { namespace: string; name: string }) => {
      const run = mustRun(req.namespace, req.name);
      run.phase = "Cancelled";
      run.completedAtUnix = unix(new Date());
      return run;
    },
    promoteAgentRun: async (req: { namespace: string; name: string }) => {
      const run = mustRun(req.namespace, req.name);
      run.phase = "Succeeded";
      run.completedAtUnix = unix(new Date());
      return run;
    },
    retryAgentRun: async (req: { namespace: string; name: string }) => {
      const run = mustRun(req.namespace, req.name);
      run.phase = "Running";
      run.retryCount += 1;
      run.lastError = "";
      return run;
    },
    renameAgentRun: async (req: { namespace: string; name: string; displayName: string }) => {
      const run = mustRun(req.namespace, req.name);
      run.displayName = req.displayName;
      return run;
    },
    deleteAgentRun: async (req: { namespace: string; name: string }) => {
      const i = s.runs.findIndex((r) => r.namespace === req.namespace && r.name === req.name);
      if (i >= 0) s.runs.splice(i, 1);
      return {};
    },
    interruptAgentRun: async () => ({}),
    updateAgentRunRuntimeConfig: async (req: { namespace: string; name: string; model: string }) => {
      const run = mustRun(req.namespace, req.name);
      if (req.model) run.resolvedModel = req.model.split("/").pop() ?? req.model;
      return run;
    },
    extendAgentRunRuntime: async (req: { namespace: string; name: string }) => mustRun(req.namespace, req.name),
    switchAgentRunMode: async (req: { namespace: string; name: string; targetMode: string }) => {
      const run = mustRun(req.namespace, req.name);
      const previous = run.modeName;
      run.modeName = req.targetMode;
      return create(SwitchAgentRunModeResponseSchema, {
        result: "applied",
        previousMode: previous,
        newMode: req.targetMode,
        revision: run.modeRevision + 1n,
      });
    },
    exportAgentRunArchive: async (req: { namespace: string; name: string }) =>
      create(ExportAgentRunArchiveResponseSchema, {
        archive: new TextEncoder().encode("selfdev fake archive"),
        filename: `${req.name}-export.zip`,
      }),

    // ---- Projects & triggers ---------------------------------------------
    listProjects: async () => create(ListProjectsResponseSchema, { projects: s.projects }),
    getProject: async (req: { namespace: string; name: string }) => {
      const p = s.projects.find((x) => x.namespace === req.namespace && x.name === req.name);
      if (!p) throw notFound(`project ${req.namespace}/${req.name}`);
      return p;
    },
    watchProjects: async function* (_req: unknown, ctx: HandlerContext) {
      for (const project of s.projects) yield create(ProjectEventSchema, { type: "ADDED", project });
      await clientGone(ctx);
    },
    listLinearProjects: async () => create(ListLinearProjectsResponseSchema, { projects: s.linearProjects }),
    getLinearProject: async (req: { namespace: string; name: string }) => {
      const p = s.linearProjects.find((x) => x.namespace === req.namespace && x.name === req.name);
      if (!p) throw notFound(`linear project ${req.namespace}/${req.name}`);
      return p;
    },
    watchLinearProjects: async function* (_req: unknown, ctx: HandlerContext) {
      for (const project of s.linearProjects) yield create(LinearProjectEventSchema, { type: "ADDED", project });
      await clientGone(ctx);
    },
    listGitHubRepositories: async () =>
      create(ListGitHubRepositoriesResponseSchema, { repositories: s.githubRepositories }),
    getGitHubRepository: async (req: { namespace: string; name: string }) => {
      const r = s.githubRepositories.find((x) => x.namespace === req.namespace && x.name === req.name);
      if (!r) throw notFound(`github repository ${req.namespace}/${req.name}`);
      return r;
    },
    watchGitHubRepositories: async function* (_req: unknown, ctx: HandlerContext) {
      for (const repository of s.githubRepositories) {
        yield create(GitHubRepositoryEventSchema, { type: "ADDED", repository });
      }
      await clientGone(ctx);
    },
    listMaintainerWorkItems: async (req: { namespace: string; repositoryName: string }) =>
      create(ListMaintainerWorkItemsResponseSchema, {
        items: s.maintainerWorkItems[runKey(req.namespace, req.repositoryName)] ?? [],
      }),
    getGitHubAppConfig: async () =>
      create(GitHubAppConfigSchema, {
        configured: true,
        appSlug: "operator-selfdev",
        installUrl: "https://github.com/apps/operator-selfdev/installations/new",
      }),
    listCrons: async () => create(ListCronsResponseSchema, { crons: s.crons }),
    getCron: async (req: { namespace: string; name: string }) => {
      const c = s.crons.find((x) => x.namespace === req.namespace && x.name === req.name);
      if (!c) throw notFound(`cron ${req.namespace}/${req.name}`);
      return c;
    },
    watchCrons: async function* (_req: unknown, ctx: HandlerContext) {
      for (const cron of s.crons) yield create(CronEventSchema, { type: "ADDED", cron });
      await clientGone(ctx);
    },

    // ---- Security ----------------------------------------------------------
    listSecurityScans: async (req: { namespace: string; scanName?: string; limit?: number }) => {
      const scans = s.securityScans.filter(
        (x) =>
          (!req.namespace || x.namespace === req.namespace) &&
          (!req.scanName || x.scanName === req.scanName),
      );
      const limit = req.limit && req.limit > 0 ? req.limit : scans.length;
      return create(ListSecurityScansResponseSchema, { scans: scans.slice(0, limit) });
    },
    getSecurityScan: async (req: { namespace: string; runName: string }) => {
      const scan = s.securityScans.find(
        (x) => x.namespace === req.namespace && x.runName === req.runName,
      );
      if (!scan) throw notFound(`security scan ${req.namespace}/${req.runName}`);
      return scan;
    },
    getSecurityScanReport: async (req: { namespace: string; runName: string; format?: string }) => {
      const scan = s.securityScans.find(
        (x) => x.namespace === req.namespace && x.runName === req.runName,
      );
      if (!scan) throw notFound(`security scan report ${req.namespace}/${req.runName}`);
      const findings = s.securityFindings.filter((f) => f.runName === scan.runName);
      const sarif = req.format === "sarif";
      return create(GetSecurityScanReportResponseSchema, {
        content: sarif ? scanReportSarif(scan, findings) : scanReportMarkdown(scan, findings),
        format: sarif ? "sarif" : "markdown",
        filename: `${scan.runName}.${sarif ? "sarif" : "md"}`,
        updatedAt: scan.completedAt ?? scan.startedAt,
      });
    },
    listSecurityScanConfigs: async (req: { namespace?: string }) =>
      create(ListSecurityScanConfigsResponseSchema, {
        configs: s.securityScanConfigs.filter((c) => !req.namespace || c.namespace === req.namespace),
      }),
    listSecurityFindings: async (req: {
      namespace: string;
      scanName?: string;
      runName?: string;
      repository?: string;
      severity?: string;
      status?: string;
      category?: string;
      search?: string;
      minScore?: number;
      includeDuplicates?: boolean;
      baselineState?: string;
      assignee?: string;
      suppressed?: string;
      limit?: number;
      offset?: number;
    }) => {
      const query = (req.search ?? "").toLowerCase();
      const findings = s.securityFindings.filter((f) => {
        if (req.namespace && f.namespace !== req.namespace) return false;
        if (req.scanName && f.scanName !== req.scanName) return false;
        if (req.runName && f.runName !== req.runName) return false;
        if (req.repository && f.repository !== req.repository) return false;
        if (req.severity && f.severity !== req.severity) return false;
        // "actionable" is a read-only grouping filter, not a persisted status.
        if (req.status === "actionable" && !ACTIONABLE_FINDING_STATUSES.has(f.status)) return false;
        if (req.status && req.status !== "actionable" && f.status !== req.status) return false;
        if (req.category && f.category !== req.category) return false;
        if (req.baselineState && f.baselineState !== req.baselineState) return false;
        if (req.assignee && f.assignee !== req.assignee) return false;
        if (req.minScore && f.score < req.minScore) return false;
        if (!req.includeDuplicates && f.duplicateOf) return false;
        if (req.suppressed === "only" && !isSuppressed(f)) return false;
        if (req.suppressed !== "only" && req.suppressed !== "include" && isSuppressed(f)) return false;
        if (query && !`${f.title} ${f.filePath} ${f.description}`.toLowerCase().includes(query)) {
          return false;
        }
        return true;
      });
      // Mirror the Postgres store: an omitted limit defaults to 200 rows.
      const offset = Math.max(req.offset ?? 0, 0);
      const limit = req.limit && req.limit > 0 ? req.limit : 200;
      return create(ListSecurityFindingsResponseSchema, {
        findings: findings.slice(offset, offset + limit),
      });
    },
    getSecurityFindingSummary: async (req: {
      namespace: string;
      scanName?: string;
      runName?: string;
      includeSuppressed?: boolean;
    }) => {
      const scoped = s.securityFindings.filter(
        (f) =>
          (!req.namespace || f.namespace === req.namespace) &&
          (!req.scanName || f.scanName === req.scanName) &&
          (!req.runName || f.runName === req.runName),
      );
      return create(GetSecurityFindingSummaryResponseSchema, {
        counts: findingCounts(scoped, req.includeSuppressed ?? false),
        trends: findingTrends(scoped),
      });
    },
    getSecurityFinding: async (req: { id: string; namespace?: string; scanName?: string }) => {
      const finding = s.securityFindings.find(
        (f) =>
          f.id === req.id &&
          (!req.namespace || f.namespace === req.namespace) &&
          (!req.scanName || f.scanName === req.scanName),
      );
      if (!finding) throw notFound(`security finding ${req.id}`);
      return create(GetSecurityFindingResponseSchema, {
        finding,
        events: findingEvents(finding, s.user.username),
      });
    },
    listSecurityFindingEvents: async (req: {
      id: string;
      namespace?: string;
      scanName?: string;
      limit?: number;
    }) => {
      const finding = s.securityFindings.find(
        (f) =>
          f.id === req.id &&
          (!req.namespace || f.namespace === req.namespace) &&
          (!req.scanName || f.scanName === req.scanName),
      );
      if (!finding) throw notFound(`security finding ${req.id}`);
      const events = findingEvents(finding, s.user.username);
      const limit = req.limit && req.limit > 0 ? req.limit : 200;
      return create(ListSecurityFindingEventsResponseSchema, { events: events.slice(0, limit) });
    },
    listSecuritySavedFilters: async (req: { namespace?: string }) =>
      create(ListSecuritySavedFiltersResponseSchema, {
        filters: s.securitySavedFilters.filter((f) => !req.namespace || f.namespace === req.namespace),
      }),
    getSecurityOverview: async (req: { namespace?: string; recentLimit?: number }) => {
      const scans = s.securityScans.filter((x) => !req.namespace || x.namespace === req.namespace);
      const findings = s.securityFindings.filter(
        (f) => !req.namespace || f.namespace === req.namespace,
      );
      const configs = s.securityScanConfigs.filter(
        (c) => !req.namespace || c.namespace === req.namespace,
      );
      const counts = findingCounts(findings, false);
      // Mirrors the dashboard server: a scan without completedAt is active,
      // the rest are recent (newest first), capped at 10 by default.
      const recentLimit = req.recentLimit && req.recentLimit > 0 ? req.recentLimit : 10;
      return create(GetSecurityOverviewResponseSchema, {
        storeSupported: true,
        activeScans: scans.filter((x) => !x.completedAt),
        recentScans: scans.filter((x) => x.completedAt).slice(0, recentLimit),
        findingCounts: counts,
        configCount: configs.length,
        configIssues: configs
          .filter((c) => c.conditionReady !== "True" || c.lastError !== "")
          .map((c) => ({
            namespace: c.namespace,
            name: c.name,
            phase: c.phase,
            readyReason: c.spec?.suspend ? "Suspended" : "CreateRunFailed",
            message: c.lastError || "Scheduling is suspended.",
            suspended: c.spec?.suspend ?? false,
          })),
        baselineAvailable: (counts["baseline_tracked"] ?? 0) > 0,
        newFindings: counts["baseline_new"] ?? 0,
        recurringFindings: counts["baseline_recurring"] ?? 0,
        resolvedFindings: counts["baseline_resolved"] ?? 0,
        regressedFindings: counts["baseline_regressed"] ?? 0,
        reopenedFindings: counts["baseline_reopened"] ?? 0,
        trends: findingTrends(findings),
      });
    },
    getSecurityConfigPostures: async (req: { namespace?: string; activityLimit?: number }) => {
      const activityLimit = req.activityLimit && req.activityLimit > 0 ? req.activityLimit : 5;
      const configs = s.securityScanConfigs.filter(
        (c) => !req.namespace || c.namespace === req.namespace,
      );
      const postures = configs
        .map((config) => {
          const runs = s.securityScans.filter((x) => x.scanName === config.name);
          const latest = runs[0];
          if (!latest) return null;
          const completed = runs.filter((x) => x.completedAt).reverse().slice(-activityLimit);
          return {
            scanName: config.name,
            findingCounts: findingCounts(
              s.securityFindings.filter((f) => f.scanName === config.name),
              false,
            ),
            repository: latest.repository,
            lastRunName: latest.runName,
            lastRunStatus: latest.status,
            lastStartedAt: latest.startedAt,
            lastCompletedAt: latest.completedAt,
            activity: completed.map((run) => ({
              runName: run.runName,
              completedAt: run.completedAt,
              severityCounts: Object.fromEntries(
                Object.entries(run.counts).filter(([key]) => SEVERITIES.has(key)),
              ),
              total: run.counts["total"] ?? 0,
            })),
          };
        })
        .filter((p) => p !== null)
        .sort((a, b) => a.scanName.localeCompare(b.scanName));
      return create(GetSecurityConfigPosturesResponseSchema, { storeSupported: true, postures });
    },
    getSecurityScanConfig: async (req: { namespace: string; name: string }) => {
      const config = s.securityScanConfigs.find(
        (x) => x.namespace === req.namespace && x.name === req.name,
      );
      if (!config) throw notFound(`security scan config ${req.namespace}/${req.name}`);
      return config;
    },
    listSecurityWorkflows: async (req: { namespace?: string }) =>
      create(ListSecurityWorkflowsResponseSchema, {
        workflows: s.securityWorkflows.filter((x) => !req.namespace || x.namespace === req.namespace),
      }),
    getSecurityWorkflow: async (req: { namespace: string; name: string }) => {
      const workflow = s.securityWorkflows.find(
        (x) => x.namespace === req.namespace && x.name === req.name,
      );
      if (!workflow) throw notFound(`security workflow ${req.namespace}/${req.name}`);
      return workflow;
    },
    listSecurityRankers: async (req: { namespace?: string }) =>
      create(ListSecurityRankersResponseSchema, {
        rankers: s.securityRankers.filter((x) => !req.namespace || x.namespace === req.namespace),
      }),
    getSecurityRanker: async (req: { namespace: string; name: string }) => {
      const ranker = s.securityRankers.find(
        (x) => x.namespace === req.namespace && x.name === req.name,
      );
      if (!ranker) throw notFound(`security ranker ${req.namespace}/${req.name}`);
      return ranker;
    },
    listSecurityPostScripts: async (req: { namespace?: string }) =>
      create(ListSecurityPostScriptsResponseSchema, {
        postScripts: s.securityPostScripts.filter(
          (x) => !req.namespace || x.namespace === req.namespace,
        ),
      }),
    getSecurityPostScript: async (req: { namespace: string; name: string }) => {
      const postScript = s.securityPostScripts.find(
        (x) => x.namespace === req.namespace && x.name === req.name,
      );
      if (!postScript) throw notFound(`security post-script ${req.namespace}/${req.name}`);
      return postScript;
    },
    listSecurityPolicyPacks: async (req: { namespace?: string }) =>
      create(ListSecurityPolicyPacksResponseSchema, {
        policyPacks: s.securityPolicyPacks.filter(
          (x) => !req.namespace || x.namespace === req.namespace,
        ),
      }),
    getSecurityPolicyPack: async (req: { namespace: string; name: string }) => {
      const pack = s.securityPolicyPacks.find(
        (x) => x.namespace === req.namespace && x.name === req.name,
      );
      if (!pack) throw notFound(`security policy pack ${req.namespace}/${req.name}`);
      return pack;
    },
    listSecurityPrograms: async (req: { namespace?: string }) =>
      create(ListSecurityProgramsResponseSchema, {
        programs: s.securityPrograms.filter((x) => !req.namespace || x.namespace === req.namespace),
      }),
    getSecurityProgram: async (req: { namespace: string; name: string }) => {
      const program = s.securityPrograms.find(
        (x) => x.namespace === req.namespace && x.name === req.name,
      );
      if (!program) throw notFound(`security program ${req.namespace}/${req.name}`);
      return program;
    },
    getSecuritySkillsStatus: async () =>
      create(SecuritySkillsStatusSchema, {
        namespace: s.namespace,
        state: s.securitySkillsInstalled ? "installed" : "not_installed",
        installedCount: s.securitySkillsInstalled ? 55 : 0,
        availableCount: 55,
      }),
    installSecuritySkills: async () => {
      s.securitySkillsInstalled = true;
      return create(SecuritySkillsStatusSchema, {
        namespace: s.namespace,
        state: "installed",
        installedCount: 55,
        availableCount: 55,
      });
    },

    // ---- Bug reports --------------------------------------------------------
    listBugReports: async (req: {
      namespace: string;
      status?: string;
      category?: string;
      limit?: number;
    }) => {
      const reports = s.bugReports.filter(
        (r) =>
          (!req.namespace || r.namespace === req.namespace) &&
          (!req.status || r.status === req.status) &&
          (!req.category || r.category === req.category),
      );
      const limit = req.limit && req.limit > 0 ? req.limit : reports.length;
      return create(ListBugReportsResponseSchema, { reports: reports.slice(0, limit) });
    },
    updateBugReportStatus: async (req: {
      namespace: string;
      id: string;
      status: string;
      note?: string;
    }) => {
      const report = s.bugReports.find(
        (r) => (!req.namespace || r.namespace === req.namespace) && r.id === req.id,
      );
      if (!report) throw notFound(`bug report ${req.namespace}/${req.id}`);
      report.status = req.status;
      report.statusNote = req.note ?? "";
      report.statusActor = s.user.username;
      return report;
    },

    // ---- Slack ------------------------------------------------------------
    listSlackAgents: async () =>
      create(ListSlackAgentsResponseSchema, { namespace: s.namespace, agents: s.slackAgents }),
    listSlackWorkspaces: async () => create(ListSlackWorkspacesResponseSchema, { workspaces: s.slackWorkspaces }),
    listSlackDrafts: async () =>
      create(ListSlackDraftsResponseSchema, { namespace: s.namespace, drafts: s.slackDrafts }),

    // ---- Skills, images, modes, models -------------------------------------
    listSkills: async () =>
      create(ListSkillsResponseSchema, { namespace: s.namespace, skills: s.skillPackages }),
    upsertSkill: async (req: UpsertSkillRequest) => {
      const skill = create(SkillInfoSchema, {
        name: req.name.trim().toLowerCase(),
        version: req.version.trim(),
        description: req.description.trim(),
        instructions: req.instructions.trim(),
        gitUrl: req.gitUrl.trim(),
        gitRef: req.gitRef.trim(),
        gitPath: req.gitPath.trim(),
        mcpServerRefs: req.mcpServerRefs,
      });
      const existing = s.skillPackages.findIndex((item) => item.name === skill.name);
      if (existing >= 0) s.skillPackages.splice(existing, 1, skill);
      else s.skillPackages.push(skill);
      s.skillPackages.sort((a, b) => a.name.localeCompare(b.name));
      return skill;
    },
    deleteSkill: async (req: { name: string }) => {
      const existing = s.skillPackages.findIndex((item) => item.name === req.name.trim().toLowerCase());
      if (existing >= 0) s.skillPackages.splice(existing, 1);
      return {};
    },
    listRuntimeImages: async () => create(ListRuntimeImagesResponseSchema, { images: s.runtimeImages }),
    listAvailableModes: async () => create(ListAvailableModesResponseSchema, { modes: s.modes }),
    getModeTemplate: async (req: { name: string }) => {
      const mode = s.modes.find((m) => m.name === req.name);
      if (!mode) throw notFound(`mode ${req.name}`);
      return mode;
    },
    listAvailableModels: async () =>
      create(ListAvailableModelsResponseSchema, {
        provider: s.models.provider,
        baseUrl: s.models.baseUrl,
        models: s.models.models,
      }),

    // ---- Settings ----------------------------------------------------------
    listMyCredentials: async () => s.credentials,
    updateMyCredentials: async () => s.credentials,
    getMyOpenAIUsage: async () => s.openAIUsage,
    getMyCopilotUsage: async () => s.copilotUsage,
    getMyAnthropicUsage: async () => s.anthropicUsage,
    startProviderOAuth: async (req: { provider: string }) =>
      req.provider === "openai"
        ? create(ProviderOAuthStartSchema, {
            provider: "openai",
            mode: "device",
            authorizeUrl: "https://auth.openai.com/device",
            userCode: "SELFDEV-OPENAI",
            intervalSeconds: 1,
            sessionId: "selfdev-openai-oauth",
          })
        : create(ProviderOAuthStartSchema, {
            provider: "anthropic",
            mode: "manual-code",
            authorizeUrl: "https://console.anthropic.com/oauth/authorize?selfdev=true",
            sessionId: "selfdev-anthropic-oauth",
          }),
    completeProviderOAuth: async () => {
      s.credentials.anthropicOauthPresent = true;
      return create(ProviderOAuthResultSchema, {
        status: "completed",
        provider: "anthropic",
        email: s.user.email,
        credentials: s.credentials,
      });
    },
    pollProviderOAuth: async () => {
      s.credentials.openaiOauthPresent = true;
      return create(ProviderOAuthResultSchema, {
        status: "completed",
        provider: "openai",
        email: s.user.email,
        credentials: s.credentials,
      });
    },
    getMySoul: async () => s.soul,
    updateMySoul: async (req: { content: string }) => {
      s.soul.content = req.content;
      return s.soul;
    },
    getMyGitIdentity: async () => s.gitIdentity,
    updateMyGitIdentity: async (req: { name: string; email: string }) => {
      s.gitIdentity.name = req.name;
      s.gitIdentity.email = req.email;
      return s.gitIdentity;
    },
    getMyModelDefaults: async () => s.modelDefaults,
    updateMyModelDefaults: async (req: {
      provider: string;
      authMode: string;
      model: string;
      reasoningLevel: string;
      disabled: boolean;
    }) => {
      const cleared = !req.provider && !req.authMode && !req.model && !req.reasoningLevel && !req.disabled;
      s.modelDefaults = cleared
        ? create(ModelDefaultsSchema, {})
        : create(ModelDefaultsSchema, {
            provider: req.provider,
            authMode: req.authMode,
            model: req.model,
            reasoningLevel: req.reasoningLevel,
            disabled: req.disabled,
            updatedAt: timestampFromDate(new Date()),
          });
      return s.modelDefaults;
    },

    // ---- Collaboration ------------------------------------------------------
    listShares: async () => create(ListSharesResponseSchema, { shares: s.shares }),
    listSharedWithMe: async () => create(ListSharedWithMeResponseSchema, { resources: s.sharedWithMe }),
    listNotifications: async (req: { unreadOnly: boolean }) =>
      create(ListNotificationsResponseSchema, {
        notifications: req.unreadOnly ? s.notifications.filter((n) => !n.read) : s.notifications,
        unreadCount: s.notifications.filter((n) => !n.read).length,
      }),
    markNotificationRead: async (req: { notificationId: string }) => {
      for (const n of s.notifications) {
        if (!req.notificationId || n.id === req.notificationId) n.read = true;
      }
      return {};
    },
    getPresence: async () => create(GetPresenceResponseSchema, { viewers: s.presenceViewers }),
    sendPresenceHeartbeat: async () => ({}),

    // ---- Observability ------------------------------------------------------
    getObservabilityOverview: async (req: {
      start?: { seconds: bigint };
      end?: { seconds: bigint };
      bucketSeconds: bigint;
    }) => {
      if (s.runs.length === 0) return create(ObservabilityOverviewResponseSchema, {});
      const startSec = Number(req.start?.seconds ?? 0n);
      const endSec = Number(req.end?.seconds ?? 0n);
      const step = Number(req.bucketSeconds || 3600n) || 3600;
      const count = Math.max(1, Math.min(2000, Math.ceil((endSec - startSec) / step)));
      // Deterministic pseudo-daily wave so screenshots are diffable.
      const wave = (i: number, base: number, swing: number) =>
        Math.max(0, base + swing * Math.sin(i / 2.1) + swing * 0.5 * Math.sin(i / 5.3 + 1.7));
      const totals = {
        runs: 0, costUsd: 0, inputTokens: 0, outputTokens: 0, toolCalls: 0, toolErrors: 0,
        subagents: 0, subagentFailures: 0, llmAttempts: 0, llmFailures: 0, compactions: 0,
        tokensReclaimed: 0, generationCostUsd: 0, generationInputTokens: 0, generationOutputTokens: 0,
      };
      const buckets = Array.from({ length: count }, (_, i) => {
        const b = {
          runs: Math.round(wave(i, 3, 2.4)),
          costUsd: wave(i, 1.9, 1.4),
          inputTokens: Math.round(wave(i, 900_000, 700_000)),
          outputTokens: Math.round(wave(i, 60_000, 45_000)),
          toolCalls: Math.round(wave(i, 340, 260)),
          toolErrors: Math.round(wave(i, 7, 6)),
          subagents: Math.round(wave(i, 22, 16)),
          subagentFailures: i % 9 === 4 ? 1 : 0,
          llmAttempts: Math.round(wave(i, 120, 90)),
          llmFailures: Math.round(wave(i, 2.2, 2)),
          compactions: Math.round(wave(i, 4, 3.4)),
          tokensReclaimed: Math.round(wave(i, 240_000, 180_000)),
          generationCostUsd: wave(i, 1.7, 1.3),
          generationInputTokens: Math.round(wave(i, 820_000, 640_000)),
          generationOutputTokens: Math.round(wave(i, 52_000, 40_000)),
        };
        for (const key of Object.keys(totals) as (keyof typeof totals)[]) totals[key] += b[key];
        return create(ObservabilityBucketSchema, {
          start: { seconds: BigInt(startSec + i * step) },
          totals: create(ObservabilityTotalsSchema, {
            ...bigintTotals(b),
            costUsd: b.costUsd,
            generationCostUsd: b.generationCostUsd,
          }),
        });
      });
      const breakdown = (
        name: string,
        count_: number,
        errors: number,
        costUsd: number,
        inputTokens: number,
        outputTokens: number,
        p95: number,
      ) =>
        create(ObservabilityBreakdownSchema, {
          name, count: BigInt(count_), errors: BigInt(errors), costUsd,
          inputTokens: BigInt(inputTokens), outputTokens: BigInt(outputTokens),
          averageDurationMs: p95 * 0.4, p95DurationMs: p95,
        });
      return create(ObservabilityOverviewResponseSchema, {
        totals: create(ObservabilityTotalsSchema, {
          ...bigintTotals(totals),
          costUsd: totals.costUsd,
          generationCostUsd: totals.generationCostUsd,
        }),
        buckets,
        tools: [
          breakdown("Bash", Math.round(totals.toolCalls * 0.34), Math.round(totals.toolErrors * 0.5), 0, 0, 0, 3_400),
          breakdown("read_file", Math.round(totals.toolCalls * 0.22), 0, 0, 0, 0, 180),
          breakdown("Edit", Math.round(totals.toolCalls * 0.16), Math.round(totals.toolErrors * 0.3), 0, 0, 0, 240),
          breakdown("grep", Math.round(totals.toolCalls * 0.12), 0, 0, 0, 0, 320),
          breakdown("subagent", Math.round(totals.toolCalls * 0.06), Math.round(totals.toolErrors * 0.2), 0, 0, 0, 210_000),
          breakdown("WebFetch", Math.round(totals.toolCalls * 0.04), 0, 0, 0, 0, 2_900),
        ],
        subagents: [
          breakdown("executor", Math.round(totals.subagents * 0.4), totals.subagentFailures, totals.generationCostUsd * 0.22, 0, 0, 260_000),
          breakdown("explore", Math.round(totals.subagents * 0.3), 0, totals.generationCostUsd * 0.08, 0, 0, 90_000),
          breakdown("code-reviewer", Math.round(totals.subagents * 0.2), 0, totals.generationCostUsd * 0.1, 0, 0, 150_000),
          breakdown("planner", Math.round(totals.subagents * 0.1), 0, totals.generationCostUsd * 0.05, 0, 0, 120_000),
        ],
        models: [
          breakdown("anthropic/claude-opus-4.6", Math.round(totals.llmAttempts * 0.52), Math.round(totals.llmFailures * 0.4), totals.generationCostUsd * 0.68, Math.round(totals.generationInputTokens * 0.6), Math.round(totals.generationOutputTokens * 0.55), 21_000),
          breakdown("openai/gpt-5.6", Math.round(totals.llmAttempts * 0.31), Math.round(totals.llmFailures * 0.35), totals.generationCostUsd * 0.24, Math.round(totals.generationInputTokens * 0.28), Math.round(totals.generationOutputTokens * 0.3), 17_000),
          breakdown("google/gemini-3-pro", Math.round(totals.llmAttempts * 0.17), Math.round(totals.llmFailures * 0.25), totals.generationCostUsd * 0.08, Math.round(totals.generationInputTokens * 0.12), Math.round(totals.generationOutputTokens * 0.15), 12_000),
        ],
        dataCompleteness: create(ObservabilityDataCompletenessSchema, {
          sessions: BigInt(s.runs.length),
          sessionsWithMetrics: BigInt(s.runs.length),
          sessionsWithActivity: BigInt(Math.max(0, s.runs.length - 1)),
          metricsComplete: true,
          activityComplete: false,
        }),
        coverageWarnings: [
          "Activity-derived counts and generation-attributed usage are best-effort because the Postgres event tee may omit events.",
          "Only currently visible AgentRuns are included; deleted historical runs are excluded.",
        ],
      });
    },

    // ---- Workspace filesystem ----------------------------------------------
    listWorkspaceFiles: async () =>
      create(ListWorkspaceFilesResponseSchema, { paths: s.workspaceFiles, truncated: false }),
    listRepositories: async () => create(ListRepositoriesResponseSchema, { repositories: s.repositories }),
    readFile: async (req: { path: string }) =>
      create(ReadFileResponseSchema, {
        content: s.fileContents[req.path] ?? `// selfdev fixture has no content for ${req.path}\n`,
        truncated: false,
      }),
  };

  return impl;
}

function buildAuthImpl(s: Scenario): AnyImpl {
  const user = () => create(UserSchema, s.user);
  const expiresAt = () => BigInt(Math.floor(Date.now() / 1000) + 3600);
  return {
    // Accepts any credentials — this backend's whole job is to be fooled.
    login: async () =>
      create(LoginResponseSchema, {
        accessToken: ACCESS_TOKEN,
        refreshToken: REFRESH_TOKEN,
        expiresAt: expiresAt(),
        user: user(),
      }),
    refreshToken: async () =>
      create(RefreshTokenResponseSchema, {
        accessToken: ACCESS_TOKEN,
        refreshToken: REFRESH_TOKEN,
        expiresAt: expiresAt(),
      }),
    logout: async () => create(LogoutResponseSchema, {}),
    getCurrentUser: async () => user(),
    searchUsers: async (req: { query: string }) =>
      create(SearchUsersResponseSchema, {
        users: [
          { id: "user-riley", email: "riley@example.com", name: "Riley Rivera", username: "riley" },
          { id: s.user.id, email: s.user.email, name: s.user.name, username: s.user.username },
        ].filter(
          (u) =>
            !req.query ||
            u.username.includes(req.query.toLowerCase()) ||
            u.name.toLowerCase().includes(req.query.toLowerCase()),
        ),
      }),
  };
}

export interface FakeBackendOptions {
  /** 0 (default) picks an ephemeral port. */
  port?: number;
  host?: string;
}

export async function startFakeBackend(
  scenario: Scenario,
  options: FakeBackendOptions = {},
): Promise<FakeBackend> {
  // Deep-clone so mutations (sent messages, renames, …) never leak between
  // server instances or test cases. Protobuf messages are plain objects.
  const s = structuredClone(scenario);

  const routes = (router: ConnectRouter) => {
    router.service(PlatformService, withDefaults(PlatformService, buildPlatformImpl(s)));
    router.service(AuthService, withDefaults(AuthService, buildAuthImpl(s)));
  };
  const rpcHandler = connectNodeAdapter({ routes });

  const server = http.createServer((req, res) => {
    const path = (req.url ?? "").split("?")[0];
    if (path === "/api/config") {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify(s.config));
      return;
    }
    if (path === "/api/version") {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({ version: "0.1.0" }));
      return;
    }
    if (path.startsWith("/api/")) {
      res.writeHead(404, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: `selfdev fake backend: no handler for ${path}` }));
      return;
    }
    rpcHandler(req, res);
  });

  const host = options.host ?? "127.0.0.1";
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(options.port ?? 0, host, () => resolve());
  });
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("fake backend: could not determine listen port");
  }

  return {
    port: address.port,
    url: `http://${host}:${address.port}`,
    scenario: s,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((err) => (err ? reject(err) : resolve()));
        // Watch streams hold connections open; sever them so close() returns.
        server.closeAllConnections();
      }),
  };
}
