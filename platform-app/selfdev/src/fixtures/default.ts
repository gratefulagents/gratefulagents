// The "default" scenario: a busy workspace exercising every major surface —
// runs in all phases (chat, plan review, team, failed, queued), activity logs
// with tool calls and sub-agents, PRs with checks, usage, diffs, traces,
// projects/triggers, Slack, skills, and settings payloads.
//
// All timestamps derive from SCENARIO_NOW so screenshots are deterministic.

import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  ActivityEntrySchema,
  AgentRunSchema,
  AgentRunUsageResponseSchema,
  ChatMessageSchema,
  CronSchema,
  GetActivityLogResponseSchema,
  GetAgentTraceResponseSchema,
  GitHubRepositorySchema,
  GitIdentitySchema,
  LinearProjectSchema,
  MaintainerWorkItemSchema,
  MyCredentialsSchema,
  MyOpenAIUsageSchema,
  NotificationInfoSchema,
  OpenAIUsageLimitSchema,
  ProjectSchema,
  ProjectTriggerSchema,
  ProjectTriggerConditionSchema,
  PullRequestDetailsSchema,
  RepositoryInfoSchema,
  ResourceShareInfoSchema,
  SharedResourceSchema,
  SkillInfoSchema,
  SlackAgentSchema,
  SlackDraftSchema,
  SlackWorkspaceSchema,
  SoulSchema,
  SubagentGraphSchema,
  TraceSpanSchema,
  UsageTotalsSchema,
  UsageTaskSchema,
} from "../../../frontend/src/rpc/platform/service_pb";
import type { Scenario } from "../scenario";
import { runKey } from "../scenario";
import { SCENARIO_NOW, minutesAgo, hoursAgo, daysAgo, unix, unixMicros } from "../time";
import {
  MODEL_LIST,
  NAMESPACE as NS,
  OWNER,
  TEAMMATE,
  USER,
  metrics,
  modeCatalog,
  runtimeImageCatalog,
} from "./common";

const REPO_URL = "https://github.com/acme/operator-app";

// ---------------------------------------------------------------------------
// Agent runs
// ---------------------------------------------------------------------------

const runUiPolish = create(AgentRunSchema, {
  namespace: NS,
  name: "run-ui-polish",
  displayName: "Polish run detail header",
  repoUrl: REPO_URL,
  baseBranch: "main",
  branchName: "chat-polish-run-header-3k2v",
  workflowMode: "chat",
  executionMode: "linear",
  model: "anthropic/claude-sonnet-4-6",
  resolvedModel: "claude-sonnet-4-6",
  modeName: "chat",
  modeCategory: "direct",
  phase: "Running",
  currentStep: "Editing RunHeader.tsx",
  sendReady: true,
  sessionNumber: 1,
  agentCount: 2,
  costUsd: "1.8420",
  inputTokens: 2_413_002n,
  outputTokens: 48_119n,
  toolCallCount: 37,
  contextTriggerTokens: 160_000n,
  contextTargetTokens: 80_000n,
  contextTokens: 74_213n,
  createdAtUnix: unix(minutesAgo(25)),
  startedAtUnix: unix(minutesAgo(24)),
  admittedAtUnix: unix(minutesAgo(24)),
  traceId: "0af7651916cd43dd8448eb211c80319c",
  activityLogUrl: "s3://operator-artifacts/demo/run-ui-polish/activity.jsonl",
  resolvedPermissionMode: "workspace-write",
  overseer: {
    modeRefName: "overseer",
    model: "anthropic/claude-opus-4-6",
    authority: "enforce",
    intervalMinutes: 10,
    maxInterventions: 5,
  },
  overseerSummary: {
    runName: "run-ui-polish-overseer",
    state: "checking",
    checkpointsHandled: 3n,
    interventionsUsed: 1,
    completionRejectionsUsed: 0,
    lastVerdict: "all_clear",
    lastSummary: "Implementation is on track.",
    lastVerdictAtUnix: unix(minutesAgo(8)),
  },
  owner: OWNER,
  myPermission: "owner",
  project: { kind: "Project", name: "operator-app" },
  conversation: [
    create(ChatMessageSchema, {
      role: "user",
      content:
        "The run detail header wraps awkwardly on narrow windows — can you tighten the layout and keep the phase pill visible?",
      timestampUnix: unix(minutesAgo(24)),
      deliveredAtUnix: unix(minutesAgo(24)),
    }),
    create(ChatMessageSchema, {
      role: "assistant",
      content:
        "Looking at `RunHeader.tsx` now. The title and metadata share one flex row; on <900px the phase pill gets pushed out. I'll split the header into a two-line layout with a `min-w-0` truncating title and pin the phase pill to the right.",
      timestampUnix: unix(minutesAgo(21)),
    }),
    create(ChatMessageSchema, {
      role: "user",
      content: "Sounds good — please also make the cost badge use the muted style.",
      timestampUnix: unix(minutesAgo(9)),
      deliveredAtUnix: unix(minutesAgo(9)),
    }),
  ],
  recentActivity: [
    { timestampUnix: unix(minutesAgo(3)), eventType: "tool_use", summary: "Edit frontend/src/components/RunHeader.tsx" },
    { timestampUnix: unix(minutesAgo(2)), eventType: "tool_use", summary: "Bash pnpm --filter @operator/frontend test" },
  ],
});

const runPlanReview = create(AgentRunSchema, {
  namespace: NS,
  name: "run-plan-review",
  displayName: "Add usage export to CSV",
  repoUrl: REPO_URL,
  baseBranch: "main",
  branchName: "plan-usage-csv-8d1q",
  workflowMode: "chat",
  executionMode: "linear",
  model: "anthropic/claude-opus-4-6",
  resolvedModel: "claude-opus-4-6",
  modeName: "plan",
  modeCategory: "direct",
  phase: "Running",
  currentStep: "Awaiting plan approval",
  sendReady: true,
  sessionNumber: 1,
  agentCount: 1,
  costUsd: "0.6110",
  inputTokens: 512_400n,
  outputTokens: 9_210n,
  toolCallCount: 12,
  createdAtUnix: unix(hoursAgo(1.2)),
  startedAtUnix: unix(hoursAgo(1.2)),
  owner: OWNER,
  myPermission: "owner",
  planSummary:
    "Add a CSV export button to the usage panel: new ExportUsageCsv RPC, a download helper, and a header action with tests.",
  planUpdatedAt: minutesAgo(6).toISOString(),
  currentPlan: [
    "# Usage CSV export",
    "",
    "## Approach",
    "1. Add `ExportUsageCsv` RPC to `rpc/platform/service.proto`.",
    "2. Serialize `UsageTask` rows server-side (attempt-level granularity).",
    "3. Frontend: `useUsageCsv` hook + download helper + header button.",
    "",
    "## Verification",
    "- Unit tests for the CSV serializer edge cases (missing tokens, subagents).",
    "- Vitest for the hook; manual snap of the usage panel.",
  ].join("\n"),
  userInputRequest: {
    type: "plan_review",
    message: "The implementation plan is ready for your review.",
    actions: [
      { id: "accept_build", label: "Accept & build", style: "primary" },
      { id: "request_changes", label: "Request changes", style: "secondary" },
    ],
  },
  pendingActions: [
    { id: "accept_build", label: "Accept & build", style: "primary" },
    { id: "request_changes", label: "Request changes", style: "secondary" },
  ],
  conversation: [
    create(ChatMessageSchema, {
      role: "user",
      content: "We need to export the per-run usage table as CSV for finance.",
      timestampUnix: unix(hoursAgo(1.2)),
      deliveredAtUnix: unix(hoursAgo(1.2)),
    }),
    create(ChatMessageSchema, {
      role: "assistant",
      content: "I've investigated the usage pipeline and drafted a plan — presenting it for review now.",
      timestampUnix: unix(minutesAgo(6)),
    }),
  ],
});

const runShipped = create(AgentRunSchema, {
  namespace: NS,
  name: "run-shipped",
  displayName: "Fix flaky reconnect test",
  repoUrl: REPO_URL,
  baseBranch: "main",
  branchName: "auto-fix-reconnect-flake-77ab",
  workflowMode: "auto",
  executionMode: "linear",
  model: "anthropic/claude-sonnet-4-6",
  resolvedModel: "claude-sonnet-4-6",
  modeName: "autopilot",
  phase: "Succeeded",
  sessionNumber: 2,
  agentCount: 3,
  costUsd: "3.1070",
  inputTokens: 5_204_112n,
  outputTokens: 121_054n,
  toolCallCount: 84,
  createdAtUnix: unix(hoursAgo(5)),
  startedAtUnix: unix(hoursAgo(5)),
  completedAtUnix: unix(hoursAgo(2)),
  pullRequestUrl: "https://github.com/acme/operator-app/pull/412",
  pullRequestUrls: ["https://github.com/acme/operator-app/pull/412"],
  owner: OWNER,
  myPermission: "owner",
  project: { kind: "Project", name: "operator-app" },
  conversation: [
    create(ChatMessageSchema, {
      role: "user",
      content: "useConnectionStatus.test.ts is flaky in CI (~1 in 20). Find the race and fix it.",
      timestampUnix: unix(hoursAgo(5)),
      deliveredAtUnix: unix(hoursAgo(5)),
    }),
    create(ChatMessageSchema, {
      role: "assistant",
      content:
        "Root cause: the reconnect backoff timer wasn't cleared before the fake timers were uninstalled. Fixed the teardown ordering and added a regression test. PR #412 is up with green checks.",
      timestampUnix: unix(hoursAgo(2)),
    }),
  ],
});

const runFailedInstall = create(AgentRunSchema, {
  namespace: NS,
  name: "run-failed-install",
  displayName: "Bump tailwind to v4.3",
  repoUrl: REPO_URL,
  baseBranch: "main",
  branchName: "auto-tailwind-bump-19zz",
  workflowMode: "auto",
  executionMode: "linear",
  model: "anthropic/claude-haiku-4-5",
  resolvedModel: "claude-haiku-4-5",
  phase: "Failed",
  retryCount: 1,
  maxRetries: 2,
  lastError:
    "pnpm install exited with code 1: ERR_PNPM_PEER_DEP_ISSUES @tailwindcss/vite@4.3.0 requires vite@^8.1.0 (workspace has 8.0.1)",
  costUsd: "0.2140",
  inputTokens: 148_211n,
  outputTokens: 3_412n,
  toolCallCount: 9,
  createdAtUnix: unix(daysAgo(1)),
  startedAtUnix: unix(daysAgo(1)),
  completedAtUnix: unix(hoursAgo(22)),
  owner: OWNER,
  myPermission: "owner",
});

const runQueued = create(AgentRunSchema, {
  namespace: NS,
  name: "run-queued",
  displayName: "Nightly dependency triage",
  repoUrl: REPO_URL,
  baseBranch: "main",
  workflowMode: "auto",
  model: "anthropic/claude-sonnet-4-6",
  phase: "Pending",
  queueState: "Queued",
  blockedReason: "Waiting for sandbox capacity (2 ahead in queue)",
  createdAtUnix: unix(minutesAgo(4)),
  trigger: { kind: "Cron", name: "nightly-triage" },
  owner: OWNER,
  myPermission: "owner",
});

const runTeamRefactor = create(AgentRunSchema, {
  namespace: NS,
  name: "run-team-refactor",
  displayName: "Extract shared table component",
  repoUrl: REPO_URL,
  baseBranch: "main",
  branchName: "team-shared-table-51mm",
  workflowMode: "auto",
  executionMode: "team",
  model: "anthropic/claude-sonnet-4-6",
  resolvedModel: "claude-sonnet-4-6",
  modeName: "team",
  modeCategory: "orchestrated",
  phase: "Running",
  currentStep: "implement",
  sessionNumber: 1,
  agentCount: 4,
  costUsd: "2.4480",
  inputTokens: 3_811_204n,
  outputTokens: 88_412n,
  toolCallCount: 61,
  createdAtUnix: unix(hoursAgo(2)),
  startedAtUnix: unix(hoursAgo(2)),
  owner: OWNER,
  myPermission: "owner",
  team: {
    steps: [
      {
        name: "plan",
        type: "sequential",
        tasks: [{ name: "plan", role: "planner", objective: "Design the extraction", dependsOn: [] }],
      },
      {
        name: "implement",
        type: "parallel",
        tasks: [
          { name: "impl-a", role: "executor", objective: "Extract shared primitives", dependsOn: ["plan"] },
          { name: "impl-b", role: "executor", objective: "Migrate consumers", dependsOn: ["plan"] },
        ],
      },
    ],
  },
  teamSummary: {
    currentStepIndex: 1,
    currentStep: "implement",
    approvalState: "approved",
    totalChildren: 3,
    runningChildren: 2,
    succeededChildren: 1,
  },
  children: [
    { name: "run-team-refactor-plan", namespace: NS, step: "plan", role: "planner", phase: "Succeeded" },
    { name: "run-team-refactor-impl-a", namespace: NS, step: "implement", role: "executor", phase: "Running" },
    { name: "run-team-refactor-impl-b", namespace: NS, step: "implement", role: "executor", phase: "Running" },
  ],
});

const runSharedByTeammate = create(AgentRunSchema, {
  namespace: "riley-rivera-8fk21",
  name: "run-onboarding-docs",
  displayName: "Refresh onboarding docs",
  repoUrl: "https://github.com/acme/handbook",
  baseBranch: "main",
  workflowMode: "chat",
  model: "anthropic/claude-sonnet-4-6",
  phase: "Succeeded",
  createdAtUnix: unix(daysAgo(2)),
  completedAtUnix: unix(daysAgo(2)),
  owner: TEAMMATE,
  myPermission: "viewer",
});

// Standing repository maintainer attached to the operator-app project's
// GitHub trigger. Carries Project provenance so it surfaces in the project's
// run list, plus the standing-run role badge.
const runMaintainer = create(AgentRunSchema, {
  namespace: NS,
  name: "project-operator-app-github-issues-maintainer",
  displayName: "Repository maintainer",
  repoUrl: REPO_URL,
  baseBranch: "main",
  workflowMode: "auto",
  executionMode: "linear",
  model: "anthropic/claude-sonnet-4-6",
  modeName: "maintainer",
  modeCategory: "supervisor",
  phase: "Running",
  currentStep: "Waiting for repository events",
  standingRunRole: "maintainer",
  sessionNumber: 4,
  agentCount: 1,
  costUsd: "0.6120",
  toolCallCount: 58,
  createdAtUnix: unix(daysAgo(6)),
  startedAtUnix: unix(daysAgo(6)),
  admittedAtUnix: unix(daysAgo(6)),
  owner: OWNER,
  myPermission: "owner",
  project: { kind: "Project", name: "operator-app" },
});

const runs = [
  runUiPolish,
  runPlanReview,
  runShipped,
  runFailedInstall,
  runQueued,
  runTeamRefactor,
  runSharedByTeammate,
  runMaintainer,
];

// ---------------------------------------------------------------------------
// Activity log, usage, PRs, diff, trace for the featured run
// ---------------------------------------------------------------------------

const uiPolishActivity = create(GetActivityLogResponseSchema, {
  isComplete: false,
  entries: [
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(24)),
      type: "system.init",
      session: 1,
      model: "claude-sonnet-4-6",
      permissionMode: "workspace-write",
      cwd: "/workspace/repo",
      version: "2.4.1",
      tools: ["Bash", "Read", "Edit", "Write", "Grep", "Glob", "WebFetch"],
      mcpServers: ["search-web"],
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(23)),
      type: "assistant",
      session: 1,
      turn: 1,
      agentName: "main",
      message:
        "I'll locate the header component and reproduce the wrapping issue before editing.",
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(22)),
      type: "tool_use",
      session: 1,
      turn: 1,
      agentName: "main",
      tool: "Grep",
      toolUseId: "toolu_01",
      input: "RunHeader",
      inputRaw: '{"pattern":"RunHeader","path":"frontend/src"}',
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(22)),
      type: "tool_result",
      session: 1,
      turn: 1,
      agentName: "main",
      tool: "Grep",
      toolUseId: "toolu_01",
      output: "frontend/src/components/RunHeader.tsx:18: export function RunHeader(",
      toolDurationMs: 141n,
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(20)),
      type: "tool_use",
      session: 1,
      turn: 2,
      agentName: "main",
      tool: "Read",
      toolUseId: "toolu_02",
      input: "frontend/src/components/RunHeader.tsx",
      inputRaw: '{"path":"frontend/src/components/RunHeader.tsx"}',
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(20)),
      type: "tool_result",
      session: 1,
      turn: 2,
      agentName: "main",
      tool: "Read",
      toolUseId: "toolu_02",
      output: "// 214 lines — header layout uses a single flex row …",
      toolDurationMs: 88n,
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(15)),
      type: "subagent_started",
      session: 1,
      agentName: "main",
      subagentType: "explore",
      taskId: "task-explore-1",
      subagentDescription: "Map every consumer of RunHeader spacing tokens",
      subagentPrompt: "Find all usages of the header spacing tokens so the refactor keeps them consistent.",
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(12)),
      type: "subagent_notification",
      session: 1,
      agentName: "explore",
      subagentType: "explore",
      taskId: "task-explore-1",
      subagentStatus: "completed",
      subagentToolCount: 6,
      subagentTotalTokens: 48_112n,
      subagentDurationMs: 172_000n,
      lastToolName: "Grep",
      subagentResultText: "3 consumers: RunHeader, RunListRow, CronDetail header.",
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(4)),
      type: "tool_use",
      session: 1,
      turn: 3,
      agentName: "main",
      tool: "Edit",
      toolUseId: "toolu_03",
      input: "frontend/src/components/RunHeader.tsx",
      inputRaw:
        '{"file_path":"frontend/src/components/RunHeader.tsx","old_string":"flex items-center gap-3","new_string":"flex flex-wrap items-center gap-x-3 gap-y-1 min-w-0"}',
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(4)),
      type: "tool_result",
      session: 1,
      turn: 3,
      agentName: "main",
      tool: "Edit",
      toolUseId: "toolu_03",
      output: "Edited 1 hunk in RunHeader.tsx",
      toolDurationMs: 64n,
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(2)),
      type: "tool_use",
      session: 1,
      turn: 4,
      agentName: "main",
      tool: "Bash",
      toolUseId: "toolu_04",
      input: "pnpm --filter @operator/frontend test",
      inputRaw: '{"command":"pnpm --filter @operator/frontend test"}',
    }),
    create(ActivityEntrySchema, {
      timestampUnix: unix(minutesAgo(1)),
      type: "tool_result",
      session: 1,
      turn: 4,
      agentName: "main",
      tool: "Bash",
      toolUseId: "toolu_04",
      output: "Test Files  24 passed (24) — Tests  183 passed (183)",
      toolDurationMs: 41_200n,
    }),
  ],
  subagentGraph: create(SubagentGraphSchema, {
    rootId: "root",
    hasSubagents: true,
    nodes: [
      {
        id: "root",
        kind: "root",
        label: "main",
        status: "running",
        lineage: "complete",
        timestampUnix: unix(minutesAgo(24)),
        entryCount: 12,
        toolCount: 4,
        totalTokens: 2_461_121n,
        model: "claude-sonnet-4-6",
      },
      {
        id: "task-explore-1",
        kind: "subagent",
        parentId: "root",
        edgeKind: "spawned",
        label: "explore",
        subtitle: "Map RunHeader spacing consumers",
        status: "succeeded",
        lineage: "complete",
        timestampUnix: unix(minutesAgo(15)),
        entryCount: 2,
        toolCount: 6,
        totalTokens: 48_112n,
        durationMs: 172_000n,
        taskId: "task-explore-1",
      },
    ],
    edges: [{ id: "e1", from: "root", to: "task-explore-1", kind: "spawned", lineage: "complete" }],
  }),
});

const totals = (input: bigint, output: bigint) =>
  create(UsageTotalsSchema, {
    inputTokens: input,
    outputTokens: output,
    cacheReadInputTokens: input / 2n,
    cacheCreationInputTokens: input / 8n,
    totalTokens: input + output,
    tokensKnown: true,
  });

const uiPolishUsage = create(AgentRunUsageResponseSchema, {
  isAvailable: true,
  isComplete: false,
  summary: totals(2_413_002n, 48_119n),
  topLevelTasks: [
    create(UsageTaskSchema, {
      taskId: "main",
      agentName: "main",
      isTopLevel: true,
      usage: totals(2_364_890n, 44_007n),
      attempts: [
        {
          attemptId: "att-1",
          agentName: "main",
          taskId: "main",
          model: "claude-sonnet-4-6",
          provider: "anthropic",
          usage: totals(2_364_890n, 44_007n),
          timestampUnix: unix(minutesAgo(2)),
          phase: "chat",
        },
      ],
    }),
  ],
  subagentTasks: [
    create(UsageTaskSchema, {
      taskId: "task-explore-1",
      agentName: "explore",
      usage: totals(46_112n, 4_112n),
      attempts: [
        {
          attemptId: "att-2",
          agentName: "explore",
          taskId: "task-explore-1",
          model: "claude-haiku-4-5",
          provider: "anthropic",
          usage: totals(46_112n, 4_112n),
          timestampUnix: unix(minutesAgo(13)),
          isSubagent: true,
        },
      ],
    }),
  ],
});

const shippedPullRequests = [
  create(PullRequestDetailsSchema, {
    url: "https://github.com/acme/operator-app/pull/412",
    repository: "acme/operator-app",
    number: 412,
    title: "Fix reconnect backoff teardown race in useConnectionStatus",
    state: "OPEN",
    headRef: "auto-fix-reconnect-flake-77ab",
    baseRef: "main",
    reviewDecision: "APPROVED",
    headSha: "f3a9c1d",
    checks: [
      { name: "frontend-test", status: "COMPLETED", conclusion: "SUCCESS" },
      { name: "typecheck", status: "COMPLETED", conclusion: "SUCCESS" },
      { name: "e2e-smoke", status: "IN_PROGRESS", conclusion: "" },
    ],
    reviewThreads: [
      {
        id: "thread-1",
        resolved: true,
        path: "frontend/src/hooks/useConnectionStatus.ts",
        line: 84,
        comments: [
          {
            author: "riley",
            body: "Nice catch — can we also assert the timer is cleared in the test?",
            createdAt: hoursAgo(3).toISOString(),
          },
          {
            author: "gratefulagents[bot]",
            body: "Added an assertion on the pending timer count in the regression test.",
            createdAt: hoursAgo(2.5).toISOString(),
          },
        ],
      },
    ],
  }),
];

const uiPolishDiff = `diff --git a/frontend/src/components/RunHeader.tsx b/frontend/src/components/RunHeader.tsx
index 8c1d2aa..f91e0b4 100644
--- a/frontend/src/components/RunHeader.tsx
+++ b/frontend/src/components/RunHeader.tsx
@@ -41,7 +41,7 @@ export function RunHeader({ run }: RunHeaderProps) {
   return (
-    <div className="flex items-center gap-3">
+    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 min-w-0">
       <h1 className="truncate text-[15px] font-semibold tracking-tight">
         {run.displayName || run.name}
       </h1>
@@ -58,7 +58,7 @@ export function RunHeader({ run }: RunHeaderProps) {
-      <Badge variant="default">{formatCost(run.costUsd)}</Badge>
+      <Badge variant="muted">{formatCost(run.costUsd)}</Badge>
     </div>
   );
 }
`;

const uiPolishTrace = create(GetAgentTraceResponseSchema, {
  traceId: "0af7651916cd43dd8448eb211c80319c",
  serviceName: "operator-agent",
  durationMs: 1_380_000n,
  isComplete: false,
  spans: [
    create(TraceSpanSchema, {
      spanId: "span-root",
      operationName: "agent.run",
      startTimeUnixUs: unixMicros(minutesAgo(24)),
      durationUs: 1_380_000_000n,
      kind: "agent",
      childCount: 4,
    }),
    create(TraceSpanSchema, {
      spanId: "span-llm-1",
      parentSpanId: "span-root",
      operationName: "llm.generation claude-sonnet-4-6",
      startTimeUnixUs: unixMicros(minutesAgo(23)),
      durationUs: 9_200_000n,
      kind: "llm.generation",
      tags: [{ key: "model", value: "claude-sonnet-4-6" }],
    }),
    create(TraceSpanSchema, {
      spanId: "span-tool-grep",
      parentSpanId: "span-root",
      operationName: "tool.grep RunHeader",
      startTimeUnixUs: unixMicros(minutesAgo(22)),
      durationUs: 141_000n,
      kind: "tool.grep",
    }),
    create(TraceSpanSchema, {
      spanId: "span-subagent",
      parentSpanId: "span-root",
      operationName: "subagent explore",
      startTimeUnixUs: unixMicros(minutesAgo(15)),
      durationUs: 172_000_000n,
      kind: "agent",
      childCount: 6,
    }),
    create(TraceSpanSchema, {
      spanId: "span-tool-bash",
      parentSpanId: "span-root",
      operationName: "tool.bash pnpm test",
      startTimeUnixUs: unixMicros(minutesAgo(2)),
      durationUs: 41_200_000n,
      kind: "tool.bash",
    }),
  ],
});

// ---------------------------------------------------------------------------
// Projects & triggers
// ---------------------------------------------------------------------------

const projects = [
  create(ProjectSchema, {
    namespace: NS,
    name: "operator-app",
    displayName: "Operator App",
    repoUrl: REPO_URL,
    baseBranch: "main",
    model: "anthropic/claude-sonnet-4-6",
    provider: "anthropic",
    authMode: "oauth",
    createdAtUnix: unix(daysAgo(40)),
    customInstructions: "Prefer small PRs. Always run frontend tests before finishing.",
    skillRefs: ["search-web"],
    permissionMode: "workspace-write",
    egressMode: "unrestricted",
    credentialStatus: { githubTokenPresent: true, anthropicApiKeyPresent: true },
    metrics: metrics(),
    owner: OWNER,
    myPermission: "owner",
    triggers: [
      create(ProjectTriggerSchema, {
        name: "github-issues",
        type: "github",
        enabled: true,
        github: {
          connectionRef: "operator-github",
          owner: "gratefulagents",
          repo: "gratefulagents",
          issues: true,
          comments: true,
          triggerKeyword: "@agent",
          pollInterval: "60s",
          maintainerEnabled: true,
          maintainerMaxDispatchesPerDay: 10,
        },
        generatedResourceName: "project-operator-app-github-issues",
        maintainerStatus: {
          runName: "project-operator-app-github-issues-maintainer",
          lastWakeUnix: unix(minutesAgo(42)),
          dispatchesToday: 3,
          lastReportTimeUnix: unix(hoursAgo(2)),
          lastReportState: "healthy",
          lastReportSummary:
            "Backlog triaged: dispatched 2 issues, 1 PR awaiting human review, no blocked work.",
        },
        observedGeneration: 4n,
        conditions: [create(ProjectTriggerConditionSchema, {
          type: "Ready",
          status: "True",
          reason: "Polling",
          message: "GitHub trigger is ready",
        })],
        lastActivityTime: timestampFromDate(minutesAgo(12)),
      }),
      create(ProjectTriggerSchema, {
        name: "nightly-maintenance",
        type: "cron",
        enabled: true,
        cron: {
          schedule: "0 2 * * *",
          timeZone: "UTC",
          concurrencyPolicy: "Forbid",
          prompt: "Review dependencies and open a small update PR.",
        },
        observedGeneration: 4n,
        conditions: [create(ProjectTriggerConditionSchema, {
          type: "Ready",
          status: "True",
          reason: "Scheduled",
          message: "Cron schedule is valid",
        })],
        nextActivityTime: timestampFromDate(hoursAgo(-5)),
      }),
      create(ProjectTriggerSchema, {
        name: "support-slack",
        type: "slack",
        enabled: false,
        slack: {
          connectionRef: "operator-slack",
          channel: "#agent-support",
          channelReplyMode: "require-approval",
        },
        observedGeneration: 4n,
        conditions: [create(ProjectTriggerConditionSchema, {
          type: "Ready",
          status: "False",
          reason: "Disabled",
          message: "Trigger is disabled",
        })],
      }),
    ],
  }),
  create(ProjectSchema, {
    namespace: NS,
    name: "widgets-api",
    displayName: "Widgets API",
    repoUrl: "https://github.com/acme/widgets-api",
    baseBranch: "develop",
    model: "anthropic/claude-haiku-4-5",
    provider: "anthropic",
    authMode: "api-key",
    createdAtUnix: unix(daysAgo(12)),
    credentialStatus: { githubTokenPresent: true, anthropicApiKeyPresent: true },
    metrics: metrics({ totalRuns: 9, successfulRuns: 8, failedRuns: 1, runningRuns: 0, totalCostUsd: 12.04 }),
    owner: OWNER,
    myPermission: "owner",
  }),
];

const linearProjects = [
  create(LinearProjectSchema, {
    namespace: NS,
    name: "linear-ops",
    projectId: "a1b2c3d4-ops",
    pollInterval: "60s",
    approvedLabel: "agent-approved",
    repoUrl: REPO_URL,
    baseBranch: "main",
    model: "anthropic/claude-sonnet-4-6",
    provider: "anthropic",
    authMode: "oauth",
    autoCreateTasks: true,
    customInstructions: "Only pick up issues labelled agent-approved.",
    lastPollTimeUnix: unix(minutesAgo(1)),
    issuesProcessed: 128,
    conditionReady: "True",
    createdAtUnix: unix(daysAgo(30)),
    metrics: metrics({ totalRuns: 128, successfulRuns: 117, failedRuns: 9, runningRuns: 2 }),
    owner: OWNER,
    myPermission: "owner",
  }),
];

const githubRepositories = [
  create(GitHubRepositorySchema, {
    namespace: NS,
    name: "gh-operator",
    owner: "acme",
    repo: "operator-app",
    repoUrl: REPO_URL,
    triggerKeyword: "@operator",
    baseBranch: "main",
    model: "anthropic/claude-sonnet-4-6",
    provider: "anthropic",
    authMode: "oauth",
    lastPollTimeUnix: unix(minutesAgo(2)),
    issuesProcessed: 54,
    conditionReady: "True",
    createdAtUnix: unix(daysAgo(21)),
    metrics: metrics({ totalRuns: 54, successfulRuns: 49, failedRuns: 3, runningRuns: 2 }),
    resourceOwner: OWNER,
    myPermission: "owner",
    triggerSettings: {
      maintainerEnabled: true,
      maintainerMaxDispatchesPerDay: 10,
    },
    maintainerStatus: {
      runName: "project-operator-app-github-issues-maintainer",
      lastWakeUnix: unix(minutesAgo(42)),
      dispatchesToday: 3,
      lastReportTimeUnix: unix(hoursAgo(2)),
      lastReportState: "healthy",
      lastReportSummary:
        "Backlog triaged: dispatched 2 issues, 1 PR awaiting human review, no blocked work.",
    },
  }),
];

// Durable maintainer work items for gh-operator, covering every dashboard
// state: pending decision, implementing with a rejected command, ready to
// merge, delivered, and closed as not actionable.
const ghOperatorWorkItems = [
    create(MaintainerWorkItemSchema, {
      namespace: NS,
      name: "gh-operator-wi-214",
      repositoryName: "gh-operator",
      issueNumber: 214,
      issueTitle: "Support SSO login for the operator console",
      issueUrl: "https://github.com/acme/operator-app/issues/214",
      issueState: "open",
      disposition: "Bounded",
      phase: "AwaitingDecision",
      evidenceSummary: "Feature is bounded to the auth module; needs a product call on the IdP.",
      pendingDecision: {
        id: "sso-idp",
        question: "Which identity provider should SSO target first?",
        options: ["Okta", "Azure AD", "Both"],
        requestedAtUnix: unix(hoursAgo(3)),
      },
      createdAtUnix: unix(daysAgo(2)),
    }),
    create(MaintainerWorkItemSchema, {
      namespace: NS,
      name: "gh-operator-wi-209",
      repositoryName: "gh-operator",
      issueNumber: 209,
      issueTitle: "Retry webhook deliveries with exponential backoff",
      issueUrl: "https://github.com/acme/operator-app/issues/209",
      issueState: "open",
      disposition: "Bounded",
      phase: "Implementing",
      agentRuns: [
        { name: "gh-operator-wi-209-impl", role: "implementer", phase: "Running", prLoopState: "reviewing" },
      ],
      pullRequests: [
        {
          repository: "acme/operator-app",
          number: 512,
          url: "https://github.com/acme/operator-app/pull/512",
          state: "open",
          checkState: "Pending",
          reviewDecision: "CHANGES_REQUESTED",
        },
      ],
      latestCommandType: "RequestMerge",
      latestCommandPhase: "Rejected",
      latestCommandMessage: "pull request has changes requested; merge readiness not met",
      createdAtUnix: unix(daysAgo(3)),
    }),
    create(MaintainerWorkItemSchema, {
      namespace: NS,
      name: "gh-operator-wi-198",
      repositoryName: "gh-operator",
      issueNumber: 198,
      issueTitle: "Fix flaky namespace cleanup in e2e suite",
      issueUrl: "https://github.com/acme/operator-app/issues/198",
      issueState: "open",
      disposition: "Bounded",
      phase: "ReadyToMerge",
      readyToMerge: true,
      pullRequests: [
        {
          repository: "acme/operator-app",
          number: 508,
          url: "https://github.com/acme/operator-app/pull/508",
          state: "open",
          checkState: "Passing",
          reviewDecision: "APPROVED",
        },
      ],
      createdAtUnix: unix(daysAgo(5)),
    }),
    create(MaintainerWorkItemSchema, {
      namespace: NS,
      name: "gh-operator-wi-186",
      repositoryName: "gh-operator",
      issueNumber: 186,
      issueTitle: "Expose run cost totals in the usage API",
      issueUrl: "https://github.com/acme/operator-app/issues/186",
      issueState: "closed",
      disposition: "Bounded",
      phase: "Delivered",
      deliverySummary: "Merged #501; usage API now returns per-run cost totals.",
      deliveredAtUnix: unix(daysAgo(1)),
      createdAtUnix: unix(daysAgo(9)),
    }),
    create(MaintainerWorkItemSchema, {
      namespace: NS,
      name: "gh-operator-wi-201",
      repositoryName: "gh-operator",
      issueNumber: 201,
      issueTitle: "Rewrite the operator in Haskell",
      issueUrl: "https://github.com/acme/operator-app/issues/201",
      issueState: "closed",
      disposition: "NotActionable",
      closeReason: "not_planned",
      phase: "Triaged",
      evidenceSummary: "Out of scope for the project; closed as not planned.",
      createdAtUnix: unix(daysAgo(4)),
    }),
];

// Both the standalone repository page and the project-generated child (the
// project page's canonical maintainer surface) share the same queue fixtures.
const maintainerWorkItems = {
  [`${NS}/gh-operator`]: ghOperatorWorkItems,
  [`${NS}/project-operator-app-github-issues`]: ghOperatorWorkItems,
};

const crons = [
  create(CronSchema, {
    namespace: NS,
    name: "nightly-triage",
    schedule: "0 3 * * *",
    timeZone: "UTC",
    concurrencyPolicy: "Forbid",
    prompt: "Triage new dependency alerts, open PRs for safe patch bumps, and summarize anything risky.",
    repoUrl: REPO_URL,
    baseBranch: "main",
    model: "anthropic/claude-sonnet-4-6",
    provider: "anthropic",
    authMode: "oauth",
    lastScheduleTimeUnix: unix(hoursAgo(14.5)),
    nextScheduleTimeUnix: unix(minutesAgo(-570)),
    runsCreated: 62,
    lastRunName: "run-queued",
    conditionReady: "True",
    createdAtUnix: unix(daysAgo(60)),
    metrics: metrics({ totalRuns: 62, successfulRuns: 58, failedRuns: 4, runningRuns: 0 }),
    defaults: {
      repoUrl: REPO_URL,
      baseBranch: "main",
      model: "anthropic/claude-sonnet-4-6",
      provider: "anthropic",
      authMode: "oauth",
      workflowMode: "auto",
      timeout: "45m",
    },
    owner: OWNER,
    myPermission: "owner",
  }),
  create(CronSchema, {
    namespace: NS,
    name: "weekly-report",
    schedule: "0 9 * * 1",
    timeZone: "America/New_York",
    suspend: true,
    concurrencyPolicy: "Forbid",
    prompt: "Write the weekly engineering velocity report from merged PRs.",
    runsCreated: 8,
    conditionReady: "True",
    createdAtUnix: unix(daysAgo(55)),
    metrics: metrics({ totalRuns: 8, successfulRuns: 8, failedRuns: 0, runningRuns: 0, totalCostUsd: 6.4 }),
    defaults: { workflowMode: "auto", model: "anthropic/claude-haiku-4-5", provider: "anthropic", authMode: "oauth" },
    owner: OWNER,
    myPermission: "owner",
  }),
];

// ---------------------------------------------------------------------------
// Slack, skills, settings
// ---------------------------------------------------------------------------

const slackAgents = [
  create(SlackAgentSchema, {
    configured: true,
    namespace: NS,
    name: "ops-agent",
    botTokenPresent: true,
    appTokenPresent: true,
    userTokenPresent: false,
    githubTokenPresent: true,
    slackUserId: "U0DANA",
    model: "claude-sonnet-4-6",
    provider: "anthropic",
    teamId: "T0ACME",
    botUserId: "B0OPS",
    ready: true,
    tokenValid: true,
    connected: true,
    permissionMode: "workspace-write",
    egressMode: "unrestricted",
    sessionIdleMinutes: 240,
    commanders: ["U0RILEY"],
    channelReplyMode: "require-approval",
    skillRefs: ["search-web"],
  }),
];

const slackWorkspaces = [
  create(SlackWorkspaceSchema, {
    namespace: NS,
    name: "acme-hq",
    botTokenPresent: true,
    appTokenPresent: true,
    teamId: "T0ACME",
    resolvedTeamId: "T0ACME",
    botUserId: "B0HQ",
    ready: true,
    tokenValid: true,
    memberCount: 3,
    mine: true,
  }),
];

const slackDrafts = [
  create(SlackDraftSchema, {
    id: "draft-1",
    channelId: "D0EXEC",
    targetUser: "U0CEO",
    incomingText: "Can you send me the Q1 infra cost breakdown?",
    draftText: "Sure — pulling the numbers now, I'll have the breakdown to you within the hour.",
    status: "pending",
    createdAtUnix: unix(minutesAgo(18)),
  }),
];

const skills = [
  create(SkillInfoSchema, {
    name: "search-web",
    version: "1.4.0",
    description: "DuckDuckGo web search MCP tools",
    instructions: "Use search for anything that needs fresh external information.",
    mcpServerRefs: ["duckduckgo"],
  }),
  create(SkillInfoSchema, {
    name: "github-tools",
    version: "0.9.2",
    description: "Extra GitHub automation tools (issues, projects)",
    gitUrl: "https://github.com/acme/github-tools-skill",
    gitRef: "main",
    phase: "Ready",
    resolvedName: "github-tools",
  }),
];

const credentials = create(MyCredentialsSchema, {
  namespace: NS,
  anthropicApiKeyPresent: true,
  anthropicOauthPresent: true,
  openaiApiKeyPresent: false,
  openaiOauthPresent: true,
  copilotOauthPresent: false,
  githubTokenPresent: true,
  integrations: [
    { name: "linear", keys: ["api-key"] },
    { name: "notion", keys: ["api-key", "workspace-id"] },
  ],
});

const openAIUsage = create(MyOpenAIUsageSchema, {
  openaiOauthPresent: true,
  accountEmail: "dana@example.com",
  planType: "pro",
  accountStatusAvailable: true,
  credits: "12.50",
  limits: [
    create(OpenAIUsageLimitSchema, {
      label: "5 hour",
      usedPercent: 42,
      resetAtUnix: unix(hoursAgo(-2)),
    }),
    create(OpenAIUsageLimitSchema, {
      label: "Weekly",
      usedPercent: 68,
      resetAtUnix: unix(daysAgo(-3)),
    }),
  ],
  tokenActivityAvailable: true,
  lifetimeTokens: 18_742_901n,
  peakDailyTokens: 1_208_440n,
  currentStreakDays: 9n,
  longestStreakDays: 24n,
  longestRunningTurnSeconds: 5_460n,
  last30DaysTokens: 7_842_118n,
  lookbackDays: 30,
  fetchedAtUnix: unix(SCENARIO_NOW),
});

const soul = create(SoulSchema, {
  content: [
    "# SOUL",
    "",
    "- Bias to small, verifiable steps; show your work.",
    "- Never leave a PR without green frontend tests.",
    "- Prefer boring, obvious code over clever code.",
  ].join("\n"),
  updatedAt: timestampFromDate(daysAgo(3)),
});

const gitIdentity = create(GitIdentitySchema, {
  name: "Dana Demo",
  email: "dana@example.com",
  updatedAt: timestampFromDate(daysAgo(10)),
});

// ---------------------------------------------------------------------------
// Collaboration
// ---------------------------------------------------------------------------

const shares = [
  create(ResourceShareInfoSchema, {
    id: "share-1",
    resourceType: "agent_run",
    resourceId: "run-ui-polish",
    resourceNamespace: NS,
    sharedWith: TEAMMATE,
    sharedBy: OWNER,
    permission: "collaborator",
    createdAt: timestampFromDate(hoursAgo(4)),
  }),
];

const sharedWithMe = [
  create(SharedResourceSchema, {
    share: create(ResourceShareInfoSchema, {
      id: "share-2",
      resourceType: "agent_run",
      resourceId: "run-onboarding-docs",
      resourceNamespace: "riley-rivera-8fk21",
      sharedWith: OWNER,
      sharedBy: TEAMMATE,
      permission: "viewer",
      createdAt: timestampFromDate(daysAgo(2)),
    }),
    displayName: "Refresh onboarding docs",
    status: "Succeeded",
  }),
];

const notifications = [
  create(NotificationInfoSchema, {
    id: "notif-1",
    type: "share",
    title: "Riley shared a run with you",
    body: "Refresh onboarding docs (viewer)",
    resourceType: "agent_run",
    resourceId: "run-onboarding-docs",
    resourceNamespace: "riley-rivera-8fk21",
    actor: TEAMMATE,
    read: false,
    createdAt: timestampFromDate(daysAgo(2)),
  }),
  create(NotificationInfoSchema, {
    id: "notif-2",
    type: "run_completed",
    title: "Fix flaky reconnect test succeeded",
    body: "PR #412 opened with green checks",
    resourceType: "agent_run",
    resourceId: "run-shipped",
    resourceNamespace: NS,
    actor: OWNER,
    read: true,
    createdAt: timestampFromDate(hoursAgo(2)),
  }),
];

// ---------------------------------------------------------------------------
// Workspace files
// ---------------------------------------------------------------------------

const workspaceFiles = [
  "README.md",
  "Makefile",
  "frontend/src/App.tsx",
  "frontend/src/components/RunHeader.tsx",
  "frontend/src/components/AgentRunDetail.tsx",
  "frontend/src/hooks/useConnectionStatus.ts",
  "frontend/src/lib/format.ts",
  "rpc/platform/service.proto",
  "web/vite.config.ts",
  "tauri/src-tauri/tauri.conf.json",
];

const repositories = [
  create(RepositoryInfoSchema, {
    name: "repo",
    path: "/workspace/repo",
    remoteUrl: REPO_URL,
    branch: "chat-polish-run-header-3k2v",
    isPrimary: true,
  }),
  create(RepositoryInfoSchema, {
    name: "widgets",
    path: "/workspace/repo/repos/widgets",
    remoteUrl: "https://github.com/acme/widgets-api",
    branch: "main",
  }),
];

const fileContents: Record<string, string> = {
  "README.md": "# operator-app\n\nDemo repository used by the selfdev fixtures.\n",
  "frontend/src/components/RunHeader.tsx":
    "export function RunHeader() {\n  return <header className=\"flex flex-wrap items-center gap-x-3\" />;\n}\n",
};

// ---------------------------------------------------------------------------
// Scenario
// ---------------------------------------------------------------------------

export const defaultScenario: Scenario = {
  name: "default",
  description: "Busy workspace: runs in every phase, projects, triggers, Slack, skills, and settings data.",
  now: SCENARIO_NOW,
  namespace: NS,
  user: USER,
  config: { authEnabled: true, googleClientId: "" },

  runs,
  activityLogs: {
    [runKey(NS, "run-ui-polish")]: uiPolishActivity,
  },
  usage: {
    [runKey(NS, "run-ui-polish")]: uiPolishUsage,
  },
  pullRequests: {
    [runKey(NS, "run-shipped")]: shippedPullRequests,
  },
  diffs: {
    [runKey(NS, "run-ui-polish")]: uiPolishDiff,
  },
  traces: {
    [runKey(NS, "run-ui-polish")]: uiPolishTrace,
  },

  projects,
  linearProjects,
  githubRepositories,
  maintainerWorkItems,
  crons,
  slackAgents,
  slackWorkspaces,
  slackDrafts,

  skillPackages: skills,
  runtimeImages: runtimeImageCatalog(),
  modes: modeCatalog(),
  models: MODEL_LIST,
  credentials,
  openAIUsage,
  soul,
  gitIdentity,

  notifications,
  sharedWithMe,
  shares,
  presenceViewers: [TEAMMATE],

  workspaceFiles,
  repositories,
  fileContents,

  localCredentials: [
    {
      provider: "anthropic",
      label: "Anthropic OAuth",
      sourcePath: "/Users/dana/.claude/credentials.json",
      account: "dana@example.com",
      authJson: '{"access_token":"selfdev-placeholder"}',
    },
  ],

  routes: [
    { name: "home", path: "/" },
    { name: "agent-ops", path: "/runs" },
    { name: "observability", path: "/observability" },
    { name: "projects", path: "/projects" },
    { name: "project-detail", path: "/projects/demo/operator-app" },
    { name: "linear-project", path: "/linear/demo/linear-ops" },
    { name: "github-repo", path: "/github/demo/gh-operator" },
    { name: "cron-detail", path: "/cron/demo/nightly-triage" },
    { name: "slack-agent", path: "/slack/demo/ops-agent" },
    { name: "slack-agent-settings", path: "/slack/demo/ops-agent?tab=settings" },
    { name: "shared", path: "/shared" },
    { name: "run-chat-running", path: "/runs/demo/run-ui-polish" },
    { name: "run-plan-review", path: "/runs/demo/run-plan-review" },
    { name: "run-succeeded", path: "/runs/demo/run-shipped" },
    { name: "run-failed", path: "/runs/demo/run-failed-install" },
    { name: "run-team", path: "/runs/demo/run-team-refactor" },
    { name: "settings", path: "/settings" },
    { name: "settings-connection", path: "/settings/connection" },
    { name: "settings-credentials", path: "/settings/credentials" },
    { name: "settings-usage", path: "/settings/usage" },
    { name: "settings-skills", path: "/settings/skills" },
    { name: "settings-soul", path: "/settings/soul" },
    { name: "settings-git", path: "/settings/git" },
  ],
};
