// The "empty" scenario: a first-boot workspace with no resources anywhere.
// Useful for screenshotting empty states and onboarding surfaces.

import { create } from "@bufbuild/protobuf";
import {
  GitIdentitySchema,
  MyCredentialsSchema,
  MyOpenAIUsageSchema,
  SoulSchema,
} from "../../../frontend/src/rpc/platform/service_pb";
import type { Scenario } from "../scenario";
import { SCENARIO_NOW } from "../time";
import { MODEL_LIST, NAMESPACE as NS, USER, modeCatalog, runtimeImageCatalog } from "./common";

export const emptyScenario: Scenario = {
  name: "empty",
  description: "First boot: no runs, projects, triggers, or saved settings.",
  now: SCENARIO_NOW,
  namespace: NS,
  user: USER,
  config: { authEnabled: true, googleClientId: "" },

  runs: [],
  activityLogs: {},
  usage: {},
  pullRequests: {},
  diffs: {},
  traces: {},

  projects: [],
  linearProjects: [],
  githubRepositories: [],
  crons: [],
  slackAgents: [],
  slackWorkspaces: [],
  slackDrafts: [],

  skillPackages: [],
  runtimeImages: runtimeImageCatalog(),
  modes: modeCatalog(),
  models: MODEL_LIST,
  credentials: create(MyCredentialsSchema, { namespace: NS }),
  openAIUsage: create(MyOpenAIUsageSchema, { openaiOauthPresent: false, lookbackDays: 30 }),
  soul: create(SoulSchema, {}),
  gitIdentity: create(GitIdentitySchema, {}),

  notifications: [],
  sharedWithMe: [],
  shares: [],
  presenceViewers: [],

  workspaceFiles: [],
  repositories: [],
  fileContents: {},

  localCredentials: [],

  routes: [
    { name: "home", path: "/" },
    { name: "agent-ops", path: "/runs" },
    { name: "observability", path: "/observability" },
    { name: "projects", path: "/projects" },
    { name: "linear", path: "/linear" },
    { name: "github", path: "/github" },
    { name: "cron", path: "/cron" },
    { name: "slack", path: "/slack" },
    { name: "shared", path: "/shared" },
    { name: "settings", path: "/settings" },
    { name: "settings-credentials", path: "/settings/credentials" },
    { name: "settings-usage", path: "/settings/usage" },
    { name: "settings-skills", path: "/settings/skills" },
    { name: "settings-soul", path: "/settings/soul" },
    { name: "settings-git", path: "/settings/git" },
  ],
};
