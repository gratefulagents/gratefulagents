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
  ListCronsResponseSchema,
  ListGitHubRepositoriesResponseSchema,
  ListLinearProjectsResponseSchema,
  ListNotificationsResponseSchema,
  ListProjectsResponseSchema,
  ListRepositoriesResponseSchema,
  ListRuntimeImagesResponseSchema,
  ListSharesResponseSchema,
  ListSharedWithMeResponseSchema,
  ListSkillsResponseSchema,
  ListSlackAgentsResponseSchema,
  ListSlackDraftsResponseSchema,
  ListSlackWorkspacesResponseSchema,
  ListWorkspaceFilesResponseSchema,
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
  SwitchAgentRunModeResponseSchema,
  type AgentRun,
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

    // ---- Slack ------------------------------------------------------------
    listSlackAgents: async () =>
      create(ListSlackAgentsResponseSchema, { namespace: s.namespace, agents: s.slackAgents }),
    listSlackWorkspaces: async () => create(ListSlackWorkspacesResponseSchema, { workspaces: s.slackWorkspaces }),
    listSlackDrafts: async () =>
      create(ListSlackDraftsResponseSchema, { namespace: s.namespace, drafts: s.slackDrafts }),

    // ---- Skills, images, modes, models -------------------------------------
    listSkills: async () =>
      create(ListSkillsResponseSchema, { namespace: s.namespace, skills: s.skillPackages }),
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
