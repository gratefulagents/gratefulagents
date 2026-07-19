import { clone, create } from "@bufbuild/protobuf";
import { Loader2, Settings } from "lucide-react";
import { useState } from "react";

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
import {
  FlowField,
  FlowSwitchRow,
  OptionRow,
  OptionRows,
} from "@/components/create-flow/create-flow";
import { RunDefaultsRows } from "@/components/run-defaults/RunDefaultsRows";
import { UserSecretPicker } from "@/components/UserSecretPicker";
import { TriggerPolicyRows } from "@/components/run-defaults/TriggerPolicyRows";
import { emptyDefaults } from "@/components/run-defaults/helpers";
import {
  normalizeTriggerDefaults,
  resolvedTriggerPolicies,
} from "@/components/TriggerDefaultsDialog";
import { useMySecretInventory } from "@/hooks/useMySecretInventory";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import {
  AgentRunDefaultsSchema,
  GitHubRepositoryTriggerSettingsSchema,
  type AgentRunDefaults,
  type GitHubRepository,
  type GitHubRepositoryTriggerSettings,
  type TriggerPolicies,
} from "@/rpc/platform/service_pb";

/**
 * True when the defaults reference an explicit provider credential secret.
 * The trigger's GitHub token secret is deliberately ignored: token-onboarded
 * triggers always carry one, and the server preserves it independently of the
 * saved-credentials toggle.
 */
function hasExplicitProviderCredentials(defaults: AgentRunDefaults): boolean {
  return Boolean(
    defaults.claudeApiKeySecret.trim() ||
      defaults.openaiOauthSecret.trim() ||
      defaults.providerKeys.length,
  );
}

function cloneDefaults(repo: GitHubRepository): AgentRunDefaults {
  return repo.defaults ? clone(AgentRunDefaultsSchema, repo.defaults) : emptyDefaults();
}

function cloneReviewerDefaults(repo: GitHubRepository): AgentRunDefaults {
  return repo.reviewerDefaults
    ? clone(AgentRunDefaultsSchema, repo.reviewerDefaults)
    : cloneDefaults(repo);
}

function reviewerPolicySource(repo: GitHubRepository) {
  return {
    defaults: repo.reviewerDefaults,
    permissionMode: repo.reviewerPermissionMode,
    egressMode: repo.reviewerEgressMode,
    mcpPolicyDefaultAction: repo.reviewerMcpPolicyDefaultAction,
    mcpPolicyAllowedServers: repo.reviewerMcpPolicyAllowedServers,
  };
}

function cloneTriggerSettings(repo: GitHubRepository): GitHubRepositoryTriggerSettings {
  if (repo.triggerSettings) {
    return clone(GitHubRepositoryTriggerSettingsSchema, repo.triggerSettings);
  }
  return create(GitHubRepositoryTriggerSettingsSchema, {
    triggerKeyword: repo.triggerKeyword,
  });
}

function splitCSV(value: string): string[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

function normalizeTriggerSettings(settings: GitHubRepositoryTriggerSettings): GitHubRepositoryTriggerSettings {
  return create(GitHubRepositoryTriggerSettingsSchema, {
    pollInterval: settings.pollInterval.trim(),
    webhookSecret: settings.webhookSecret.trim(),
    triggerKeyword: settings.triggerKeyword.trim(),
    cancelRunsOnIssueClose: settings.cancelRunsOnIssueClose,
    authAllowedUsers: settings.authAllowedUsers.map((user) => user.trim()).filter(Boolean),
    authDenyUsers: settings.authDenyUsers.map((user) => user.trim()).filter(Boolean),
    reviewLoopDisabled: settings.reviewLoopDisabled,
    reviewLoopMaxRounds:
      settings.reviewLoopMaxRounds > 0 ? Math.trunc(settings.reviewLoopMaxRounds) : 0,
    reviewerModeRef: settings.reviewerModeRef.trim(),
    reviewerModeVersion: settings.reviewerModeVersion.trim(),
    reviewerModeChannel: settings.reviewerModeChannel.trim(),
    maintainerEnabled: settings.maintainerEnabled,
    maintainerMaxConcurrentDispatches:
      settings.maintainerMaxConcurrentDispatches > 0
        ? Math.trunc(settings.maintainerMaxConcurrentDispatches)
        : 0,
    maintainerMaxDispatchesPerDay:
      settings.maintainerMaxDispatchesPerDay > 0 ? Math.trunc(settings.maintainerMaxDispatchesPerDay) : 0,
    maintainerStandupInterval: settings.maintainerStandupInterval.trim(),
    maintainerModeRef: settings.maintainerModeRef.trim(),
    maintainerModel: settings.maintainerModel.trim(),
    maintainerAllowPrMerge: settings.maintainerAllowPrMerge,
  });
}

function triggerSettingsSummary(settings: GitHubRepositoryTriggerSettings): string {
  const parts = [settings.triggerKeyword.trim() || "@agent"];
  parts.push(`poll ${settings.pollInterval.trim() || "60s"}`);
  if (settings.webhookSecret.trim()) parts.push("webhook secret");
  if (settings.cancelRunsOnIssueClose) parts.push("cancel on close");
  if (settings.authAllowedUsers.length) parts.push(`${settings.authAllowedUsers.length} allowed`);
  if (settings.authDenyUsers.length) parts.push(`${settings.authDenyUsers.length} denied`);
  return parts.join(" · ");
}

function triggerSettingsModified(settings: GitHubRepositoryTriggerSettings): boolean {
  return Boolean(
    settings.pollInterval.trim() ||
      settings.webhookSecret.trim() ||
      (settings.triggerKeyword.trim() && settings.triggerKeyword.trim() !== "@agent") ||
      settings.cancelRunsOnIssueClose ||
      settings.authAllowedUsers.length ||
      settings.authDenyUsers.length,
  );
}

function reviewLoopSummary(
  settings: GitHubRepositoryTriggerSettings,
  useReviewerDefaults: boolean,
  reviewerDefaults: AgentRunDefaults,
): string {
  if (settings.reviewLoopDisabled ?? true) return "disabled";
  const rounds = settings.reviewLoopMaxRounds > 0 ? settings.reviewLoopMaxRounds : 3;
  const mode = settings.reviewerModeRef.trim() || "review";
  const runtime = useReviewerDefaults ? reviewerDefaults.model.trim() || "custom runtime" : "inherits run defaults";
  return `${rounds} round${rounds === 1 ? "" : "s"} · ${mode} · ${runtime}`;
}

function reviewLoopModified(settings: GitHubRepositoryTriggerSettings): boolean {
  return Boolean(
    settings.reviewLoopDisabled !== undefined ||
      settings.reviewLoopMaxRounds > 0 ||
      settings.reviewerModeRef.trim() ||
      settings.reviewerModeVersion.trim() ||
      settings.reviewerModeChannel.trim(),
  );
}

function maintainerSummary(settings: GitHubRepositoryTriggerSettings): string {
  if (!settings.maintainerEnabled) return "disabled";
  const cap = settings.maintainerMaxConcurrentDispatches > 0
    ? settings.maintainerMaxConcurrentDispatches
    : 2;
  const interval = settings.maintainerStandupInterval.trim() || "12h";
  return `${cap} concurrent · every ${interval}`;
}

function maintainerModified(settings: GitHubRepositoryTriggerSettings): boolean {
  return Boolean(
    settings.maintainerEnabled ||
      settings.maintainerMaxConcurrentDispatches > 0 ||
      settings.maintainerMaxDispatchesPerDay > 0 ||
      settings.maintainerStandupInterval.trim() ||
      settings.maintainerModeRef.trim() ||
      settings.maintainerModel.trim() ||
      settings.maintainerAllowPrMerge,
  );
}

/**
 * Edit dialog for an existing GitHubRepository trigger. The repository identity
 * and GitHub auth wiring are fixed at onboarding, but every other configurable
 * CRD field exposed to users is editable here: trigger/webhook/auth settings,
 * autonomous PR review-loop settings, run defaults, and managed runtime/MCP
 * policies.
 */
export function GitHubRepositorySettingsDialog({
  repo,
  onUpdated,
}: {
  repo: GitHubRepository;
  onUpdated?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [defaults, setDefaults] = useState<AgentRunDefaults>(() => cloneDefaults(repo));
  const [policies, setPolicies] = useState<TriggerPolicies>(() => resolvedTriggerPolicies(repo));
  const [triggerSettings, setTriggerSettings] = useState<GitHubRepositoryTriggerSettings>(() =>
    cloneTriggerSettings(repo),
  );
  const [useReviewerDefaults, setUseReviewerDefaults] = useState(() => Boolean(repo.reviewerDefaults));
  const [reviewerDefaults, setReviewerDefaults] = useState<AgentRunDefaults>(() => cloneReviewerDefaults(repo));
  const [reviewerPolicies, setReviewerPolicies] = useState<TriggerPolicies>(() =>
    resolvedTriggerPolicies(reviewerPolicySource(repo)),
  );
  const [useSavedReviewerCredentials, setUseSavedReviewerCredentials] = useState(
    () => !hasExplicitProviderCredentials(repo.reviewerDefaults ?? repo.defaults ?? emptyDefaults()),
  );
  const [useSavedCredentials, setUseSavedCredentials] = useState(
    () => !hasExplicitProviderCredentials(repo.defaults ?? emptyDefaults()),
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const secretInventory = useMySecretInventory(repo.namespace);

  function reset() {
    setDefaults(cloneDefaults(repo));
    setPolicies(resolvedTriggerPolicies(repo));
    setTriggerSettings(cloneTriggerSettings(repo));
    setUseReviewerDefaults(Boolean(repo.reviewerDefaults));
    setReviewerDefaults(cloneReviewerDefaults(repo));
    setReviewerPolicies(resolvedTriggerPolicies(reviewerPolicySource(repo)));
    setUseSavedReviewerCredentials(
      !hasExplicitProviderCredentials(repo.reviewerDefaults ?? repo.defaults ?? emptyDefaults()),
    );
    setUseSavedCredentials(!hasExplicitProviderCredentials(repo.defaults ?? emptyDefaults()));
    setError(null);
  }

  function updateTriggerSettings(patch: Partial<GitHubRepositoryTriggerSettings>) {
    setTriggerSettings((prev) => ({ ...prev, ...patch }));
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!defaults.model.trim()) {
      setError("Choose a model — runs created by this trigger require an explicit model.");
      return;
    }
    if (useReviewerDefaults && !reviewerDefaults.model.trim()) {
      setError("Choose a reviewer model or turn off separate reviewer settings.");
      return;
    }
    const normalizedTriggerSettings = normalizeTriggerSettings(triggerSettings);
    if (
      !normalizedTriggerSettings.reviewerModeRef &&
      (normalizedTriggerSettings.reviewerModeVersion || normalizedTriggerSettings.reviewerModeChannel)
    ) {
      setError("Set a reviewer mode before adding a reviewer mode version or channel.");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      await client.updateGitHubRepository({
        namespace: repo.namespace,
        name: repo.name,
        defaults: normalizeTriggerDefaults(defaults, useSavedCredentials),
        policies,
        useSavedCredentials,
        triggerSettings: normalizedTriggerSettings,
        useReviewerDefaults,
        reviewerDefaults: useReviewerDefaults
          ? {
              ...normalizeTriggerDefaults(reviewerDefaults, useSavedReviewerCredentials),
              workflowMode: "",
              executionMode: "",
              modeRef: "",
            }
          : undefined,
        reviewerPolicies: useReviewerDefaults ? reviewerPolicies : undefined,
        useSavedReviewerCredentials,
      });
      setOpen(false);
      onUpdated?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save GitHub trigger settings");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (nextOpen) reset();
      }}
    >
      <DialogTrigger render={
        <Button variant="outline" size="sm">
          <Settings className="size-3.5" />
          Settings
        </Button>
      } />
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Settings className="size-4" />
              </span>
              <DialogTitle className="text-base">Settings — {repo.name}</DialogTitle>
            </div>
            <DialogDescription>
              Trigger settings and defaults applied to runs this repository creates.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <OptionRows label="GitHub trigger" className="pt-1">
              <OptionRow
                icon={Settings}
                title="Trigger settings"
                summary={triggerSettingsSummary(triggerSettings)}
                modified={triggerSettingsModified(triggerSettings)}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="github-settings-trigger-keyword"
                    label="Trigger keyword"
                    hint="Bot mention used in issue and PR comments. Empty uses @agent."
                  >
                    <Input
                      id="github-settings-trigger-keyword"
                      value={triggerSettings.triggerKeyword}
                      onChange={(event) => updateTriggerSettings({ triggerKeyword: event.target.value })}
                      placeholder="@agent"
                    />
                  </FlowField>
                  <FlowField
                    id="github-settings-poll-interval"
                    label="Poll interval"
                    hint="How often to scan issues for mode labels. Empty uses 60s."
                  >
                    <Input
                      id="github-settings-poll-interval"
                      value={triggerSettings.pollInterval}
                      onChange={(event) => updateTriggerSettings({ pollInterval: event.target.value })}
                      placeholder="60s"
                    />
                  </FlowField>
                  <FlowField
                    id="github-settings-webhook-secret"
                    label="Webhook secret"
                    hint="Secret name for classic per-resource GitHub webhooks."
                  >
                    <UserSecretPicker
                      id="github-settings-webhook-secret"
                      value={triggerSettings.webhookSecret}
                      secrets={secretInventory.secrets}
                      loading={secretInventory.loading}
                      onOpen={() => void secretInventory.reload()}
                      onChange={(webhookSecret) => updateTriggerSettings({ webhookSecret })}
                    />
                  </FlowField>
                </div>
                <FlowSwitchRow
                  id="github-settings-cancel-on-close"
                  label="Cancel active runs when issues close"
                  hint="Closing or deleting an issue requests graceful cancellation of active runs from it."
                  control={
                    <Switch
                      id="github-settings-cancel-on-close"
                      checked={triggerSettings.cancelRunsOnIssueClose}
                      onCheckedChange={(checked) =>
                        updateTriggerSettings({ cancelRunsOnIssueClose: checked })
                      }
                    />
                  }
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="github-settings-auth-allowed"
                    label="Allowed GitHub users"
                    hint="Comma-separated GitHub logins. Empty allows owners, members, and collaborators."
                  >
                    <Input
                      id="github-settings-auth-allowed"
                      value={triggerSettings.authAllowedUsers.join(", ")}
                      onChange={(event) =>
                        updateTriggerSettings({ authAllowedUsers: splitCSV(event.target.value) })
                      }
                      placeholder="octocat, monalisa"
                    />
                  </FlowField>
                  <FlowField
                    id="github-settings-auth-deny"
                    label="Denied GitHub users"
                    hint="Comma-separated GitHub logins. Deny list wins over allow list."
                  >
                    <Input
                      id="github-settings-auth-deny"
                      value={triggerSettings.authDenyUsers.join(", ")}
                      onChange={(event) =>
                        updateTriggerSettings({ authDenyUsers: splitCSV(event.target.value) })
                      }
                      placeholder="bad-actor"
                    />
                  </FlowField>
                </div>
              </OptionRow>

              <OptionRow
                icon={Settings}
                title="PR review loop"
                summary={reviewLoopSummary(triggerSettings, useReviewerDefaults, reviewerDefaults)}
                modified={reviewLoopModified(triggerSettings) || useReviewerDefaults}
              >
                <FlowSwitchRow
                  id="github-settings-review-disabled"
                  label="Disable autonomous PR review loop"
                  hint="When off, pull-based monitoring gives agent-created PRs reviewer runs and automatic request-changes resolution rounds."
                  control={
                    <Switch
                      id="github-settings-review-disabled"
                      checked={triggerSettings.reviewLoopDisabled ?? true}
                      onCheckedChange={(checked) =>
                        updateTriggerSettings({ reviewLoopDisabled: checked })
                      }
                    />
                  }
                />
                <FlowSwitchRow
                  id="github-settings-reviewer-defaults"
                  label="Use separate reviewer settings"
                  hint="Choose a dedicated model, provider credentials, runtime, tools, instructions, and policies. Repository checkout and GitHub identity stay fixed."
                  control={
                    <Switch
                      id="github-settings-reviewer-defaults"
                      checked={useReviewerDefaults}
                      onCheckedChange={setUseReviewerDefaults}
                    />
                  }
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="github-settings-review-max-rounds"
                    label="Max review rounds"
                    hint="Blank uses the controller default of 3."
                  >
                    <Input
                      id="github-settings-review-max-rounds"
                      type="number"
                      min={1}
                      value={
                        triggerSettings.reviewLoopMaxRounds > 0
                          ? String(triggerSettings.reviewLoopMaxRounds)
                          : ""
                      }
                      onChange={(event) =>
                        updateTriggerSettings({
                          reviewLoopMaxRounds: event.target.value ? Number(event.target.value) : 0,
                        })
                      }
                      placeholder="3"
                    />
                  </FlowField>
                  <FlowField
                    id="github-settings-reviewer-mode"
                    label="Reviewer mode"
                    hint="ModeTemplate name for reviewer runs. Empty uses review."
                  >
                    <Input
                      id="github-settings-reviewer-mode"
                      value={triggerSettings.reviewerModeRef}
                      onChange={(event) => updateTriggerSettings({ reviewerModeRef: event.target.value })}
                      placeholder="review"
                    />
                  </FlowField>
                  <FlowField
                    id="github-settings-reviewer-version"
                    label="Reviewer mode version"
                  >
                    <Input
                      id="github-settings-reviewer-version"
                      value={triggerSettings.reviewerModeVersion}
                      onChange={(event) =>
                        updateTriggerSettings({ reviewerModeVersion: event.target.value })
                      }
                      placeholder="Optional"
                    />
                  </FlowField>
                  <FlowField
                    id="github-settings-reviewer-channel"
                    label="Reviewer mode channel"
                  >
                    <Input
                      id="github-settings-reviewer-channel"
                      value={triggerSettings.reviewerModeChannel}
                      onChange={(event) =>
                        updateTriggerSettings({ reviewerModeChannel: event.target.value })
                      }
                      placeholder="stable"
                    />
                  </FlowField>
                </div>
              </OptionRow>
            </OptionRows>

            <OptionRows label="Maintainer" className="pt-1">
              <OptionRow
                icon={Settings}
                title="Maintainer"
                summary={maintainerSummary(triggerSettings)}
                modified={maintainerModified(triggerSettings)}
              >
                <FlowSwitchRow
                  id="github-settings-maintainer-enabled"
                  label="Enable maintainer"
                  hint="A standing agent that triages this repository's issues, dispatches implementer runs by labeling issues, watches them, and reports."
                  control={
                    <Switch
                      id="github-settings-maintainer-enabled"
                      checked={triggerSettings.maintainerEnabled ?? false}
                      onCheckedChange={(checked) =>
                        updateTriggerSettings({ maintainerEnabled: checked })
                      }
                    />
                  }
                />
                {triggerSettings.maintainerEnabled ? (
                  <div className="space-y-5">
                    <div className="space-y-3">
                      <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground/80">
                        Dispatch limits
                      </p>
                      <div className="grid gap-4 sm:grid-cols-2">
                        <FlowField
                          id="github-settings-maintainer-max-concurrent"
                          label="Max concurrent dispatches"
                          hint="Blank uses the controller default of 2."
                        >
                          <Input
                            id="github-settings-maintainer-max-concurrent"
                            type="number"
                            min={1}
                            value={
                              triggerSettings.maintainerMaxConcurrentDispatches > 0
                                ? String(triggerSettings.maintainerMaxConcurrentDispatches)
                                : ""
                            }
                            onChange={(event) =>
                              updateTriggerSettings({
                                maintainerMaxConcurrentDispatches: event.target.value
                                  ? Number(event.target.value)
                                  : 0,
                              })
                            }
                            placeholder="2"
                          />
                        </FlowField>
                        <FlowField
                          id="github-settings-maintainer-max-per-day"
                          label="Max dispatches per day"
                          hint="Blank uses the controller default of 10."
                        >
                          <Input
                            id="github-settings-maintainer-max-per-day"
                            type="number"
                            min={1}
                            value={
                              triggerSettings.maintainerMaxDispatchesPerDay > 0
                                ? String(triggerSettings.maintainerMaxDispatchesPerDay)
                                : ""
                            }
                            onChange={(event) =>
                              updateTriggerSettings({
                                maintainerMaxDispatchesPerDay: event.target.value
                                  ? Number(event.target.value)
                                  : 0,
                              })
                            }
                            placeholder="10"
                          />
                        </FlowField>
                      </div>
                    </div>
                    <div className="space-y-3">
                      <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground/80">
                        Schedule
                      </p>
                      <div className="grid gap-4 sm:grid-cols-2">
                        <FlowField
                          id="github-settings-maintainer-standup-interval"
                          label="Standup interval"
                          hint="Go duration. Blank uses 12h."
                        >
                          <Input
                            id="github-settings-maintainer-standup-interval"
                            value={triggerSettings.maintainerStandupInterval}
                            onChange={(event) =>
                              updateTriggerSettings({ maintainerStandupInterval: event.target.value })
                            }
                            placeholder="12h"
                          />
                        </FlowField>
                      </div>
                    </div>
                    <div className="space-y-3">
                      <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground/80">
                        Runtime
                      </p>
                      <div className="grid gap-4 sm:grid-cols-2">
                        <FlowField
                          id="github-settings-maintainer-mode"
                          label="Maintainer mode"
                          hint="ModeTemplate name. Blank uses maintainer."
                        >
                          <Input
                            id="github-settings-maintainer-mode"
                            value={triggerSettings.maintainerModeRef}
                            onChange={(event) =>
                              updateTriggerSettings({ maintainerModeRef: event.target.value })
                            }
                            placeholder="maintainer"
                          />
                        </FlowField>
                        <FlowField
                          id="github-settings-maintainer-model"
                          label="Maintainer model"
                          hint="Blank inherits the repository run model."
                        >
                          <Input
                            id="github-settings-maintainer-model"
                            value={triggerSettings.maintainerModel}
                            onChange={(event) =>
                              updateTriggerSettings({ maintainerModel: event.target.value })
                            }
                            placeholder="Optional"
                          />
                        </FlowField>
                      </div>
                    </div>
                    <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2.5">
                      <FlowSwitchRow
                        id="github-settings-maintainer-allow-pr-merge"
                        label="Allow the maintainer to merge approved pull requests"
                        hint="Danger: this lets the maintainer merge approved, non-draft pull requests without a human merge step."
                        control={
                          <Switch
                            id="github-settings-maintainer-allow-pr-merge"
                            checked={triggerSettings.maintainerAllowPrMerge}
                            onCheckedChange={(checked) =>
                              updateTriggerSettings({ maintainerAllowPrMerge: checked })
                            }
                          />
                        }
                      />
                    </div>
                  </div>
                ) : null}
              </OptionRow>
            </OptionRows>

            {useReviewerDefaults ? (
              <OptionRows label="Reviewer defaults" className="pt-1">
                <RunDefaultsRows
                  idPrefix="github-settings-reviewer"
                  hideRepository
                  hideOrchestration
                  showRepositoryOptions
                  hideGitHubCredentials
                  resourceNamespace={repo.namespace}
                  value={reviewerDefaults}
                  onChange={setReviewerDefaults}
                  useSavedCredentials={useSavedReviewerCredentials}
                  onUseSavedCredentialsChange={setUseSavedReviewerCredentials}
                />
                <TriggerPolicyRows
                  idPrefix="github-settings-reviewer-policies"
                  policies={reviewerPolicies}
                  onPoliciesChange={setReviewerPolicies}
                  runtimeProfileRef={reviewerDefaults.runtimeProfileRef}
                  onRuntimeProfileRefChange={(ref) =>
                    setReviewerDefaults((prev) => ({ ...prev, runtimeProfileRef: ref }))
                  }
                  mcpPolicyRef={reviewerDefaults.mcpPolicyRef}
                  onMcpPolicyRefChange={(ref) =>
                    setReviewerDefaults((prev) => ({ ...prev, mcpPolicyRef: ref }))
                  }
                />
              </OptionRows>
            ) : null}

            <OptionRows label="Implementer run defaults" className="pt-1">
              <RunDefaultsRows
                idPrefix="github-settings"
                hideRepository
                resourceNamespace={repo.namespace}
                value={defaults}
                onChange={setDefaults}
                useSavedCredentials={useSavedCredentials}
                onUseSavedCredentialsChange={setUseSavedCredentials}
              />
              <TriggerPolicyRows
                idPrefix="github-settings-policies"
                policies={policies}
                onPoliciesChange={setPolicies}
                runtimeProfileRef={defaults.runtimeProfileRef}
                onRuntimeProfileRefChange={(ref) =>
                  setDefaults((prev) => ({ ...prev, runtimeProfileRef: ref }))
                }
                mcpPolicyRef={defaults.mcpPolicyRef}
                onMcpPolicyRefChange={(ref) =>
                  setDefaults((prev) => ({ ...prev, mcpPolicyRef: ref }))
                }
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
              {submitting ? "Saving…" : "Save changes"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
