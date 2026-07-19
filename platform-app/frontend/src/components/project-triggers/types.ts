export type TriggerSource = "github" | "slack" | "cron" | "linear";
export type ConnectionType = Exclude<TriggerSource, "cron">;

type TriggerFields = Record<string, unknown>;

export interface ProjectConnection {
  name: string;
  type: ConnectionType;
  github?: {
    tokenSecret?: string;
    appId?: bigint | number;
    installationId?: bigint | number;
    privateKeySecret?: string;
    /** Write-only raw PAT. Server stores it and fills tokenSecret. */
    token?: string;
    /** Write-only raw PEM. Server stores it and fills privateKeySecret. */
    privateKey?: string;
  };
  slack?: {
    tokensSecret?: string;
    teamId?: string;
    /** Write-only xoxb- bot token. */
    botToken?: string;
    /** Write-only xapp- app token. */
    appToken?: string;
  };
  linear?: {
    apiKeySecret?: string;
    workspaceId?: string;
    /** Write-only raw Linear API key. */
    apiKey?: string;
  };
}

export interface ProjectTrigger {
  name: string;
  type: string;
  enabled?: boolean;
  github?: TriggerFields;
  slack?: TriggerFields;
  cron?: TriggerFields;
  linear?: TriggerFields;
  observedGeneration?: bigint;
  conditions?: Array<Record<string, unknown>>;
  lastActivityTime?: Record<string, unknown>;
  nextActivityTime?: Record<string, unknown>;
  lastError?: string;
}

export interface ProjectWithTriggers {
  triggers?: ProjectTrigger[];
}

export interface ProjectTriggerClient {
  listConnections(request: { namespace: string }): Promise<{ connections?: ProjectConnection[] }>;
  createConnection(request: {
    namespace: string;
    name: string;
    connection: ProjectConnection;
  }): Promise<unknown>;
  updateConnection(request: {
    namespace: string;
    name: string;
    connection: ProjectConnection;
  }): Promise<unknown>;
  deleteConnection(request: {
    namespace: string;
    name: string;
  }): Promise<unknown>;
  createProjectTrigger(request: {
    namespace: string;
    project: string;
    name: string;
    trigger: ProjectTrigger;
  }): Promise<unknown>;
  updateProjectTrigger(request: {
    namespace: string;
    project: string;
    name: string;
    trigger: ProjectTrigger;
  }): Promise<unknown>;
  deleteProjectTrigger(request: {
    namespace: string;
    project: string;
    name: string;
  }): Promise<unknown>;
  setProjectTriggerEnabled(request: {
    namespace: string;
    project: string;
    name: string;
    enabled: boolean;
  }): Promise<unknown>;
}

export function triggerSource(trigger: ProjectTrigger): TriggerSource {
  if (trigger.type === "slack") return "slack";
  if (trigger.type === "cron") return "cron";
  if (trigger.type === "linear") return "linear";
  return "github";
}
