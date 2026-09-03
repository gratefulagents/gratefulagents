import { useState } from "react";
import { Link } from "react-router-dom";
import { Eye, EyeOff, ShieldCheck } from "lucide-react";

import { Chip, FlowField, FlowSwitchRow, Segmented } from "@/components/create-flow/create-flow";
import { PROVIDERS, providerMeta } from "@/components/create-flow/providers";
import { MCPServerPicker } from "@/components/MCPServerPicker";
import { ModeTemplateSelect } from "@/components/ModeTemplateSelect";
import { RepoUrlListInput } from "@/components/RepoUrlListInput";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { UserSecretKeyPicker, UserSecretPicker } from "@/components/UserSecretPicker";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { REASONING_LEVELS } from "@/lib/reasoning";
import { mcpPolicyBlocksServers } from "@/lib/resourceNames";
import { toneText } from "@/lib/status";
import { cn } from "@/lib/utils";

import { splitCommaList } from "./projectForm";
import type { ProjectFormController } from "./useProjectForm";

/**
 * Field groups shared by the create dialog and the in-place settings panel.
 * Each renders only its controls; the host decides the frame (collapsed
 * OptionRow at create time, always-visible section on the project page) so
 * create and edit can never drift apart again.
 */

export const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

type Props = { c: ProjectFormController };

/* ── Repository ───────────────────────────────────────────────── */

export function RepositoryUrlField({ c, autoFocus, hint }: Props & { autoFocus?: boolean; hint?: string }) {
  const id = `${c.idPrefix}-repo-url`;
  return (
    <FlowField id={id} label="Repository URL" hint={hint}>
      <Input
        id={id}
        value={c.form.repoUrl}
        onChange={(event) => c.update("repoUrl", event.target.value)}
        placeholder="https://github.com/org/repo"
        autoFocus={autoFocus}
        inputMode="url"
        autoComplete="off"
        spellCheck={false}
      />
    </FlowField>
  );
}

export function RepositoryDetailsFields({ c }: Props) {
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        <FlowField
          id={`${c.idPrefix}-base-branch`}
          label="Base branch"
          hint="Branch runs start from and open pull requests against."
        >
          <Input
            id={`${c.idPrefix}-base-branch`}
            value={c.form.baseBranch}
            onChange={(event) => c.update("baseBranch", event.target.value)}
            placeholder="main"
          />
        </FlowField>
      </div>
      <FlowField
        id={`${c.idPrefix}-additional-repo-0`}
        label="Additional repositories"
        hint="Extra repos cloned into every run alongside the primary one."
      >
        <RepoUrlListInput
          idPrefix={`${c.idPrefix}-additional-repo`}
          value={c.form.additionalRepoUrls}
          onChange={(urls) => c.update("additionalRepoUrls", urls)}
        />
      </FlowField>
    </>
  );
}

/* ── Model & credentials ──────────────────────────────────────── */

export function ModelFields({ c }: Props) {
  const { form, mode } = c;
  const meta = providerMeta(form.provider);
  const isCopilot = form.provider === "copilot";
  const modelOptions = c.models;
  const catalogActive = mode === "edit" || (c.useSaved && c.savedReady);
  const modelId = `${c.idPrefix}-model`;
  const listId = `${modelId}-options`;

  return (
    <>
      <div className="flex flex-wrap gap-1.5" role="group" aria-label="Provider">
        {PROVIDERS.map((p) => (
          <Chip key={p.id} selected={form.provider === p.id} onSelect={() => c.setProvider(p.id)}>
            {p.name}
          </Chip>
        ))}
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <FlowField id={modelId} label="Model" required={isCopilot}>
          <Input
            id={modelId}
            value={form.model}
            onChange={(event) => c.update("model", event.target.value)}
            placeholder={
              modelOptions.length ? "Choose a model" : isCopilot ? "gpt-4.1" : "Provider default"
            }
            list={modelOptions.length ? listId : undefined}
          />
          {modelOptions.length > 0 ? (
            <datalist id={listId}>
              {modelOptions.map((model) => (
                <option key={model} value={model} />
              ))}
            </datalist>
          ) : null}
          {catalogActive && (c.modelsLoading || c.modelsError || modelOptions.length > 0 || isCopilot) ? (
            <p className="text-[11px] text-muted-foreground" aria-live="polite">
              {c.modelsLoading
                ? `Loading ${meta.name} models…`
                : c.modelsError
                  ? `Could not load ${meta.name} models: ${c.modelsError}`
                  : modelOptions.length
                    ? `${modelOptions.length} ${meta.name} models available`
                    : c.savedReady
                      ? "Enter a Copilot model name."
                      : "Connect Copilot in Settings to load models."}
            </p>
          ) : null}
        </FlowField>
        <FlowField id={`${c.idPrefix}-reasoning-level`} label="Reasoning level">
          <select
            id={`${c.idPrefix}-reasoning-level`}
            value={form.reasoningLevel}
            onChange={(event) => c.update("reasoningLevel", event.target.value)}
            className={selectClassName}
          >
            {REASONING_LEVELS.map((level) => (
              <option key={level || "default"} value={level}>
                {level || "default"}
              </option>
            ))}
          </select>
        </FlowField>
      </div>

      {c.oauthSupported ? (
        <FlowSwitchRow
          label="Authentication"
          control={
            <Segmented
              aria-label="Authentication"
              value={form.authMode}
              onChange={(v) => c.update("authMode", v)}
              options={[
                { value: "api-key", label: "API key" },
                { value: "oauth", label: "OAuth" },
              ]}
            />
          }
        />
      ) : (
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          {meta.oauthOnly
            ? `${meta.name} uses OAuth — sign in once under Settings and reuse it here.`
            : `${meta.name} uses API-key authentication.`}
        </p>
      )}

      {c.savedSupported ? (
        <FlowSwitchRow
          id={`${c.idPrefix}-use-saved`}
          label="Use my saved credentials"
          hint={
            c.useSaved ? (
              c.savedReady ? (
                <span className="inline-flex items-center gap-1.5">
                  <ShieldCheck className="size-3.5 shrink-0 text-[color:var(--tone-success-fg)]" />
                  Your saved {meta.name} credential
                  {mode === "create" && c.credentials.githubToken ? " and GitHub token" : ""} will be
                  used, stored privately in your namespace.
                </span>
              ) : (
                <span>
                  No saved {meta.name} credential yet —{" "}
                  <Link to="/settings/credentials" className="underline underline-offset-4">
                    add it in Settings
                  </Link>{" "}
                  or turn this off to {mode === "create" ? "enter one below" : "reference a Secret"}.
                </span>
              )
            ) : mode === "edit" ? (
              "Reference Kubernetes Secrets in the project's namespace instead."
            ) : undefined
          }
          control={
            <Switch
              id={`${c.idPrefix}-use-saved`}
              checked={form.useSavedCredentials}
              onCheckedChange={(checked) => c.update("useSavedCredentials", checked)}
            />
          }
        />
      ) : null}

      {!c.useSaved ? (
        mode === "create" ? (
          <CreateCredentialInputs c={c} />
        ) : (
          <EditCredentialRefs c={c} />
        )
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2">
        <FlowField
          id={`${c.idPrefix}-allowed-models`}
          label="Allowed models"
          hint="Optional allow-list runs may pick from. Comma-separated."
        >
          <Input
            id={`${c.idPrefix}-allowed-models`}
            value={form.allowedModels}
            onChange={(event) => c.update("allowedModels", event.target.value)}
            placeholder="model-a, model-b"
          />
        </FlowField>
      </div>
    </>
  );
}

function CreateCredentialInputs({ c }: Props) {
  const { form } = c;
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <FlowField id={`${c.idPrefix}-github-token`} label="GitHub token" hint="Optional, for cloning.">
        <SecretInput
          id={`${c.idPrefix}-github-token`}
          value={form.githubToken}
          onChange={(event) => c.update("githubToken", event.target.value)}
          placeholder="ghp_… / github_pat_…"
        />
      </FlowField>
      {c.effectiveAuthMode === "oauth" ? (
        <FlowField
          id={`${c.idPrefix}-oauth-secret`}
          label="OAuth secret"
          hint="Name of an existing Secret containing auth.json."
        >
          <UserSecretPicker
            id={`${c.idPrefix}-oauth-secret`}
            value={form.openaiOauthSecret}
            secrets={c.userSecrets}
            onOpen={() => void c.refreshUserSecrets()}
            onChange={(secretName) => c.update("openaiOauthSecret", secretName)}
          />
        </FlowField>
      ) : form.provider === "anthropic" ? (
        <FlowField id={`${c.idPrefix}-anthropic-api-key`} label="Anthropic API key">
          <SecretInput
            id={`${c.idPrefix}-anthropic-api-key`}
            value={form.anthropicApiKey}
            onChange={(event) => c.update("anthropicApiKey", event.target.value)}
            placeholder="sk-ant-…"
          />
        </FlowField>
      ) : (
        <FlowField
          id={`${c.idPrefix}-openai-api-key`}
          label={form.provider === "openai" ? "OpenAI API key" : "API key (OpenAI-compatible)"}
        >
          <SecretInput
            id={`${c.idPrefix}-openai-api-key`}
            value={form.openaiApiKey}
            onChange={(event) => c.update("openaiApiKey", event.target.value)}
            placeholder="sk-…"
          />
        </FlowField>
      )}
    </div>
  );
}

function EditCredentialRefs({ c }: Props) {
  const { form } = c;
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      {c.effectiveAuthMode === "oauth" ? (
        <FlowField id={`${c.idPrefix}-oauth-secret`} label="OAuth Secret">
          <UserSecretPicker
            id={`${c.idPrefix}-oauth-secret`}
            value={form.openaiOauthSecret}
            secrets={c.userSecrets}
            onOpen={() => void c.refreshUserSecrets()}
            onChange={(secretName) => c.update("openaiOauthSecret", secretName)}
          />
        </FlowField>
      ) : (
        <>
          {form.provider === "anthropic" ? (
            <FlowField id={`${c.idPrefix}-anthropic-secret`} label="Anthropic Secret">
              <UserSecretPicker
                id={`${c.idPrefix}-anthropic-secret`}
                value={form.claudeApiKeySecret}
                secrets={c.userSecrets}
                onOpen={() => void c.refreshUserSecrets()}
                onChange={(secretName) => c.update("claudeApiKeySecret", secretName)}
              />
            </FlowField>
          ) : null}
          <FlowField id={`${c.idPrefix}-provider-secret`} label="Provider key Secret">
            <UserSecretPicker
              id={`${c.idPrefix}-provider-secret`}
              value={form.providerKeySecret}
              secrets={c.userSecrets}
              onOpen={() => void c.refreshUserSecrets()}
              onChange={(secretName) => {
                c.setForm((prev) => ({ ...prev, providerKeySecret: secretName, providerKeyKey: "" }));
              }}
            />
          </FlowField>
          <FlowField id={`${c.idPrefix}-provider-key`} label="Provider key field">
            <UserSecretKeyPicker
              id={`${c.idPrefix}-provider-key`}
              value={form.providerKeyKey}
              secretName={form.providerKeySecret}
              secrets={c.userSecrets}
              onChange={(secretKey) => c.update("providerKeyKey", secretKey)}
            />
          </FlowField>
        </>
      )}
      <FlowField id={`${c.idPrefix}-github-secret`} label="GitHub token Secret">
        <UserSecretPicker
          id={`${c.idPrefix}-github-secret`}
          value={form.githubTokenSecret}
          secrets={c.userSecrets}
          onOpen={() => void c.refreshUserSecrets()}
          onChange={(secretName) => c.update("githubTokenSecret", secretName)}
        />
      </FlowField>
    </div>
  );
}

/* ── Agent behavior ───────────────────────────────────────────── */

export function AgentFields({ c, enabled = true }: Props & { enabled?: boolean }) {
  const { form, mode } = c;
  return (
    <>
      <FlowField
        id={`${c.idPrefix}-mode-ref`}
        label="Default mode"
        hint="Behavior and tool policy new runs start with. Runs can still switch modes."
      >
        <ModeTemplateSelect
          id={`${c.idPrefix}-mode-ref`}
          value={form.modeRef}
          enabled={enabled}
          onChange={(value) => c.update("modeRef", value)}
        />
      </FlowField>
      <FlowSwitchRow
        id={`${c.idPrefix}-review-loop`}
        label="Autonomous PR review loop"
        hint="Runs review and iterate on the pull requests they open, including PRs in additional repositories."
        control={
          <Switch
            id={`${c.idPrefix}-review-loop`}
            checked={!form.reviewLoopDisabled}
            onCheckedChange={(checked) => c.update("reviewLoopDisabled", !checked)}
          />
        }
      />
      <FlowField
        id={`${c.idPrefix}-custom-instructions`}
        label="Custom instructions"
        hint="Project-specific guidance every run receives."
      >
        <Textarea
          id={`${c.idPrefix}-custom-instructions`}
          value={form.customInstructions}
          onChange={(event) => c.update("customInstructions", event.target.value)}
          className="min-h-24"
          placeholder="Use pnpm, keep PRs small, follow CONTRIBUTING.md…"
        />
      </FlowField>
      {mode === "edit" ? (
        <FlowSwitchRow
          id={`${c.idPrefix}-bug-squasher`}
          label="Default bug squasher"
          hint="Make this the namespace's default project for automated bug fixes: moving an agent-filed bug report to in progress launches an autonomous fix run from here, and the report resolves when the fix PR merges. Enabling this clears the flag on every other project in the namespace."
          control={
            <Switch
              id={`${c.idPrefix}-bug-squasher`}
              checked={form.bugSquasher}
              onCheckedChange={(checked) => c.update("bugSquasher", checked)}
            />
          }
        />
      ) : null}
    </>
  );
}

/* ── Runtime ──────────────────────────────────────────────────── */

export function RuntimeFields({ c }: Props) {
  const { form } = c;
  const namePlaceholder = form.name.trim() ? `${form.name.trim()}-runtime` : "project-runtime";
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        <FlowField id={`${c.idPrefix}-image`} label="Runtime image" hint="Toolchains available to the agent.">
          <RuntimeImagePicker
            id={`${c.idPrefix}-image`}
            value={form.image}
            onChange={(image) => c.update("image", image)}
          />
        </FlowField>
        <FlowField id={`${c.idPrefix}-timeout`} label="Timeout" hint="Maximum run duration, e.g. 30m or 2h.">
          <Input
            id={`${c.idPrefix}-timeout`}
            value={form.timeout}
            onChange={(event) => c.update("timeout", event.target.value)}
            placeholder="30m"
          />
        </FlowField>
      </div>
      <FlowSwitchRow
        id={`${c.idPrefix}-configure-runtime`}
        label={c.mode === "create" ? "Create a RuntimeProfile" : "Create/update a RuntimeProfile"}
        hint="Controls sandbox permissions and network egress for this project's runs."
        control={
          <Switch
            id={`${c.idPrefix}-configure-runtime`}
            checked={form.configureRuntimeProfile}
            onCheckedChange={(checked) => c.update("configureRuntimeProfile", checked)}
          />
        }
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <FlowField id={`${c.idPrefix}-runtime-profile-ref`} label="RuntimeProfile ref">
          <Input
            id={`${c.idPrefix}-runtime-profile-ref`}
            value={form.runtimeProfileRef}
            onChange={(event) => c.update("runtimeProfileRef", event.target.value)}
            placeholder={namePlaceholder}
          />
        </FlowField>
        {form.configureRuntimeProfile ? (
          <>
            <FlowField id={`${c.idPrefix}-permission-mode`} label="Permission mode">
              <select
                id={`${c.idPrefix}-permission-mode`}
                value={form.permissionMode}
                onChange={(event) => c.update("permissionMode", event.target.value)}
                className={selectClassName}
              >
                <option value="read-only">read-only</option>
                <option value="workspace-write">workspace-write</option>
                <option value="danger-full-access">danger-full-access</option>
              </select>
            </FlowField>
            <FlowField id={`${c.idPrefix}-egress-mode`} label="Network egress">
              <select
                id={`${c.idPrefix}-egress-mode`}
                value={form.egressMode}
                onChange={(event) => c.update("egressMode", event.target.value)}
                className={selectClassName}
              >
                <option value="unrestricted">unrestricted</option>
                <option value="restricted">restricted</option>
                <option value="disabled">disabled</option>
              </select>
            </FlowField>
          </>
        ) : null}
      </div>
    </>
  );
}

/* ── Tools & MCP policy ───────────────────────────────────────── */

export function ToolsFields({ c }: Props) {
  const { form } = c;
  const allowed = splitCommaList(form.mcpPolicyAllowedServers);
  const namePlaceholder = form.name.trim() ? `${form.name.trim()}-mcp-policy` : "project-mcp-policy";
  return (
    <>
      <FlowField label="MCP servers" hint="Server configs attached to this project's runs.">
        <MCPServerPicker
          selected={form.mcpServerRefs}
          onChange={(names) => c.update("mcpServerRefs", names)}
        />
      </FlowField>
      {mcpPolicyBlocksServers(
        form.configureMcpPolicy,
        form.mcpPolicyDefaultAction,
        allowed,
        form.mcpServerRefs,
      ) && (
        <p className={cn("text-[12px]", toneText.warning)}>
          Your MCP policy denies by default — add the selected server names to its allowed servers
          or their tools won't load.
        </p>
      )}
      <FlowSwitchRow
        id={`${c.idPrefix}-configure-mcp-policy`}
        label={c.mode === "create" ? "Create an MCPPolicy" : "Create/update an MCPPolicy"}
        hint="Restricts which MCP servers this project's runs may reach."
        control={
          <Switch
            id={`${c.idPrefix}-configure-mcp-policy`}
            checked={form.configureMcpPolicy}
            onCheckedChange={(checked) => c.update("configureMcpPolicy", checked)}
          />
        }
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <FlowField id={`${c.idPrefix}-mcp-policy-ref`} label="MCPPolicy ref">
          <Input
            id={`${c.idPrefix}-mcp-policy-ref`}
            value={form.mcpPolicyRef}
            onChange={(event) => c.update("mcpPolicyRef", event.target.value)}
            placeholder={namePlaceholder}
          />
        </FlowField>
        {form.configureMcpPolicy ? (
          <>
            <FlowField id={`${c.idPrefix}-mcp-policy-default-action`} label="Default action">
              <select
                id={`${c.idPrefix}-mcp-policy-default-action`}
                value={form.mcpPolicyDefaultAction}
                onChange={(event) => c.update("mcpPolicyDefaultAction", event.target.value)}
                className={selectClassName}
              >
                <option value="Deny">Deny</option>
                <option value="Allow">Allow</option>
              </select>
            </FlowField>
            <FlowField id={`${c.idPrefix}-mcp-policy-allowed`} label="Allowed MCP servers" hint="Comma-separated.">
              <Input
                id={`${c.idPrefix}-mcp-policy-allowed`}
                value={form.mcpPolicyAllowedServers}
                onChange={(event) => c.update("mcpPolicyAllowedServers", event.target.value)}
                placeholder="fetch, github"
              />
            </FlowField>
          </>
        ) : null}
      </div>
    </>
  );
}

/* ── Privileged access (admins) ───────────────────────────────── */

export function PrivilegedFields({ c }: Props) {
  const { form } = c;
  return (
    <>
      <FlowSwitchRow
        id={`${c.idPrefix}-kubernetes-admin`}
        label="Kubernetes admin"
        hint="Grant this project's runs cluster-admin RBAC and read-only platform introspection tools."
        control={
          <Switch
            id={`${c.idPrefix}-kubernetes-admin`}
            checked={form.kubernetesAdmin}
            onCheckedChange={(checked) => c.update("kubernetesAdmin", checked)}
          />
        }
      />
      <FlowSwitchRow
        id={`${c.idPrefix}-docker-in-docker`}
        label="Docker-in-Docker"
        hint="Run a privileged docker:dind sidecar in this project's run pods so agents can use docker build, run, and compose. Requires the cluster to allow privileged pods in the runs namespace."
        control={
          <Switch
            id={`${c.idPrefix}-docker-in-docker`}
            checked={form.dockerInDocker}
            onCheckedChange={(checked) => c.update("dockerInDocker", checked)}
          />
        }
      />
    </>
  );
}

/* ── Secret input ─────────────────────────────────────────────── */

export function SecretInput(props: React.ComponentProps<typeof Input>) {
  const [revealed, setRevealed] = useState(false);
  return (
    <div className="relative">
      <Input
        {...props}
        type={revealed ? "text" : "password"}
        autoComplete="new-password"
        className={cn("pr-9", props.className)}
      />
      <button
        type="button"
        className="absolute right-2 top-1/2 -translate-y-1/2 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        onClick={() => setRevealed((value) => !value)}
        aria-label={revealed ? "Hide secret" : "Reveal secret"}
      >
        {revealed ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  );
}
