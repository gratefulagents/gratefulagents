// Scenario shape consumed by the fake backend and the screenshot CLI.
//
// Fixtures are built with the generated protobuf schemas from
// frontend/src/rpc, so they type-check against the real API contract and
// break loudly when the proto evolves.

import type {
  AgentRun,
  AgentRunUsageResponse,
  Cron,
  GetActivityLogResponse,
  GetAgentTraceResponse,
  GitHubRepository,
  GitIdentity,
  LinearProject,
  ModeTemplate,
  MyCredentials,
  MyOpenAIUsage,
  NotificationInfo,
  Project,
  PullRequestDetails,
  RepositoryInfo,
  ResourceOwner,
  ResourceShareInfo,
  RuntimeImageOption,
  SharedResource,
  SkillInfo,
  SlackAgent,
  SlackDraft,
  SlackWorkspace,
  Soul,
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
  crons: Cron[];
  slackAgents: SlackAgent[];
  slackWorkspaces: SlackWorkspace[];
  slackDrafts: SlackDraft[];

  skillPackages: SkillInfo[];
  runtimeImages: RuntimeImageOption[];
  modes: ModeTemplate[];
  models: { provider: string; baseUrl: string; models: string[] };
  credentials: MyCredentials;
  openAIUsage: MyOpenAIUsage;
  soul: Soul;
  gitIdentity: GitIdentity;

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
