// Scenario shape consumed by the fake backend and the screenshot CLI.
//
// Fixtures are built with the generated protobuf schemas from
// frontend/src/rpc, so they type-check against the real API contract and
// break loudly when the proto evolves.

import type {
  AgentRun,
  AgentRunUsageResponse,
  BugReport,
  Cron,
  GetActivityLogResponse,
  GetAgentTraceResponse,
  GitHubRepository,
  GitIdentity,
  ModelDefaults,
  LinearProject,
  MaintainerWorkItem,
  ModeTemplate,
  MyAnthropicUsage,
  MyCredentials,
  MyCopilotUsage,
  MyOpenAIUsage,
  NotificationInfo,
  Project,
  PullRequestDetails,
  RepositoryInfo,
  ResourceOwner,
  ResourceShareInfo,
  RuntimeImageOption,
  SecurityCatalog,
  SecurityFinding,
  SecurityPolicyPackResource,
  SecurityPostScriptResource,
  SecurityProgramResource,
  SecurityRankerResource,
  SecuritySavedFilter,
  SecurityScan,
  SecurityScanConfig,
  SecurityWorkflowResource,
  SharedResource,
  SkillInfo,
  SlackAgent,
  SlackDraft,
  SlackWorkspace,
  Soul,
  SSHTunnel,
} from "../../frontend/src/rpc/platform/service_pb";

/** Matches the AuthUser shape AuthContext stores (auth.v1.User). */
export interface ScenarioUser {
  id: string;
  username: string;
  email: string;
  name: string;
  picture: string;
  role: string;
}

/** One credential as returned by the native detect_local_credentials command. */
export interface ScenarioLocalCredential {
  provider: string;
  label: string;
  sourcePath: string;
  account: string | null;
  authJson: string;
}

/** A route worth screenshotting, used by `snap-all`. */
export interface ScenarioRoute {
  /** Output file slug, e.g. "run-detail-running". */
  name: string;
  /** App route, e.g. "/runs/demo/run-ui-polish". */
  path: string;
}

export interface Scenario {
  name: string;
  description: string;
  /** Frozen clock all fixture timestamps are relative to. */
  now: Date;
  /** Personal namespace all fixtures live in. */
  namespace: string;
  user: ScenarioUser;
  /** Served at GET /api/config. */
  config: { authEnabled: boolean; googleClientId: string };

  runs: AgentRun[];
  /** Keyed by `${namespace}/${name}`. */
  activityLogs: Record<string, GetActivityLogResponse>;
  usage: Record<string, AgentRunUsageResponse>;
  pullRequests: Record<string, PullRequestDetails[]>;
  diffs: Record<string, string>;
  traces: Record<string, GetAgentTraceResponse>;

  projects: Project[];
  linearProjects: LinearProject[];
  githubRepositories: GitHubRepository[];
  /** Keyed by `${namespace}/${githubRepositoryName}`. */
  maintainerWorkItems: Record<string, MaintainerWorkItem[]>;
  crons: Cron[];
  slackAgents: SlackAgent[];
  slackWorkspaces: SlackWorkspace[];
  slackDrafts: SlackDraft[];

  /** Persisted security scan RESULT rows (getSecurityScan / listSecurityScans). */
  securityScans: SecurityScan[];
  /** Deduplicated finding rows (listSecurityFindings / getSecurityFindingSummary). */
  securityFindings: SecurityFinding[];
  /** Configured SecurityScan triggers, including lastExecution state. */
  securityScanConfigs: SecurityScanConfig[];
  /** Reusable SecurityWorkflow library resources (workflowRef targets). */
  securityWorkflows: SecurityWorkflowResource[];
  /** Reusable SecurityRanker library resources (rankerRefs targets). */
  securityRankers: SecurityRankerResource[];
  /** Reusable SecurityPostScript library resources (postScriptRefs targets). */
  securityPostScripts: SecurityPostScriptResource[];
  /** Reusable SecurityPolicyPack library resources (policyPackRef targets). */
  securityPolicyPacks: SecurityPolicyPackResource[];
  /** Operator-verified SecurityProgram resources (securityProgramRef targets). */
  securityPrograms: SecurityProgramResource[];
  /** Saved findings-filter queries (listSecuritySavedFilters). */
  securitySavedFilters: SecuritySavedFilter[];
  /** Whether the current user has explicitly installed the curated security bundle. */
  securitySkillsInstalled: boolean;
  /** Shipped, opt-in security catalog exposed by the manager namespace. */
  securityCatalog: SecurityCatalog;

  /** Agent-filed platform bug reports (listBugReports / updateBugReportStatus). */
  bugReports: BugReport[];

  skillPackages: SkillInfo[];
  runtimeImages: RuntimeImageOption[];
  /** kubectl-authored SSH tunnels surfaced by listSSHTunnels. */
  sshTunnels: SSHTunnel[];
  modes: ModeTemplate[];
  models: { provider: string; baseUrl: string; models: string[] };
  credentials: MyCredentials;
  openAIUsage: MyOpenAIUsage;
  copilotUsage: MyCopilotUsage;
  anthropicUsage: MyAnthropicUsage;
  soul: Soul;
  gitIdentity: GitIdentity;
  /** Personal default provider/auth mode/model/reasoning (getMyModelDefaults). */
  modelDefaults: ModelDefaults;

  notifications: NotificationInfo[];
  sharedWithMe: SharedResource[];
  shares: ResourceShareInfo[];
  presenceViewers: ResourceOwner[];

  workspaceFiles: string[];
  repositories: RepositoryInfo[];
  fileContents: Record<string, string>;

  /** Fed to the Tauri-sim `detect_local_credentials` stub. */
  localCredentials: ScenarioLocalCredential[];

  /** Routes covered by `snap-all` (param routes point at fixture resources). */
  routes: ScenarioRoute[];
}

export function runKey(namespace: string, name: string): string {
  return `${namespace}/${name}`;
}
