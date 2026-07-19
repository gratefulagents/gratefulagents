import { useEffect, useId, useState, type ReactElement, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { Plus } from "lucide-react";
import { client } from "@/lib/client";
import { PROVIDERS } from "@/components/create-flow/providers";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  buildLinearCreateRequest,
  initialLinearCreateValues,
  type LinearCreateValues,
} from "@/components/linear-create";

const selectClassName =
  "flex h-8 w-full rounded-lg border border-input bg-transparent px-2.5 py-1 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50";

export function CreateLinearProjectDialog({
  onCreated,
  trigger,
}: {
  onCreated?: () => void;
  trigger?: ReactElement;
}) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState(initialLinearCreateValues);
  const [credentialsNamespace, setCredentialsNamespace] = useState("");
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = <K extends keyof LinearCreateValues>(key: K, value: LinearCreateValues[K]) =>
    setForm((current) => ({ ...current, [key]: value }));
  const effectiveAuthMode = form.provider === "copilot" ? "oauth" : form.authMode;

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    void client
      .listMyCredentials({}, { signal: controller.signal })
      .then((credentials) => {
        if (!controller.signal.aborted) setCredentialsNamespace(credentials.namespace);
      })
      .catch(() => {
        if (!controller.signal.aborted) setCredentialsNamespace("");
      });
    return () => controller.abort();
  }, [open]);

  useEffect(() => {
    if (!open || !credentialsNamespace) return;
    const controller = new AbortController();
    const provider = form.provider;

    void (async () => {
      setAvailableModels([]);
      setModelsLoading(true);
      setModelsError(null);
      try {
        const response = await client.listAvailableModels(
          {
            namespace: credentialsNamespace,
            provider,
            authMode: effectiveAuthMode,
          },
          { signal: controller.signal },
        );
        if (!controller.signal.aborted) setAvailableModels(response.models);
      } catch (cause) {
        if (controller.signal.aborted) return;
        setAvailableModels([]);
        setModelsError(cause instanceof Error ? cause.message : "Failed to load provider models");
      } finally {
        if (!controller.signal.aborted) setModelsLoading(false);
      }
    })();

    return () => controller.abort();
  }, [open, credentialsNamespace, form.provider, effectiveAuthMode]);

  function setProvider(provider: string) {
    setForm((current) => ({
      ...current,
      provider,
      authMode: provider === "copilot" ? "oauth" : current.authMode,
    }));
  }

  async function submit() {
    setSaving(true);
    setError(null);
    try {
      const project = await client.createLinearProject(buildLinearCreateRequest(form));
      setOpen(false);
      setForm(initialLinearCreateValues);
      onCreated?.();
      navigate(`/linear/${project.namespace}/${project.name}`);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  const incomplete = !form.name.trim() || !form.linearApiKey.trim() ||
    !form.projectId.trim() || !form.teamId.trim() || !form.model.trim();

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={trigger ?? <Button><Plus className="size-4" />Create project</Button>} />
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Create Linear project</DialogTitle>
          <DialogDescription>
            Connect a Linear project and choose the defaults and managed policies used for new runs.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5">
          <FormSection title="Linear connection">
            <Field label="Resource name" value={form.name} onChange={(value) => set("name", value)} required />
            <Field label="Linear API key" value={form.linearApiKey} onChange={(value) => set("linearApiKey", value)} secret required />
            <Field label="Project ID" value={form.projectId} onChange={(value) => set("projectId", value)} required />
            <Field label="Team ID" value={form.teamId} onChange={(value) => set("teamId", value)} required />
            <Field label="Poll interval" value={form.pollInterval} onChange={(value) => set("pollInterval", value)} />
            <Field label="Approved label" value={form.approvedLabel} onChange={(value) => set("approvedLabel", value)} />
            <Toggle label="Automatically create tasks" checked={form.autoCreateTasks} onChange={(value) => set("autoCreateTasks", value)} />
          </FormSection>

          <FormSection title="Run defaults">
            <ModelField
              value={form.model}
              onChange={(value) => set("model", value)}
              models={availableModels}
              loading={modelsLoading}
              error={modelsError}
            />
            <SelectField
              id="linear-create-provider"
              label="Provider"
              value={form.provider}
              onChange={setProvider}
              options={PROVIDERS.map((provider) => ({ value: provider.id, label: provider.name }))}
            />
            <SelectField
              id="linear-create-auth-mode"
              label="Authentication mode"
              value={effectiveAuthMode}
              onChange={(value) => set("authMode", value)}
              options={
                form.provider === "copilot"
                  ? [{ value: "oauth", label: "OAuth" }]
                  : [
                      { value: "api-key", label: "API key" },
                      { value: "oauth", label: "OAuth" },
                    ]
              }
            />
            <p className="self-end text-xs text-muted-foreground sm:col-span-2">
              Runs use the provider and GitHub credentials saved in your personal namespace.
            </p>
          </FormSection>

          <FormSection title="Optional managed policies">
            <Toggle label="Create a managed runtime profile" checked={form.configureRuntimeProfile} onChange={(value) => set("configureRuntimeProfile", value)} />
            {form.configureRuntimeProfile && (
              <>
                <Field label="Permission mode" value={form.permissionMode} onChange={(value) => set("permissionMode", value)} />
                <Field label="Egress mode" value={form.egressMode} onChange={(value) => set("egressMode", value)} />
              </>
            )}
            <Toggle label="Create a managed MCP policy" checked={form.configureMcpPolicy} onChange={(value) => set("configureMcpPolicy", value)} />
            {form.configureMcpPolicy && (
              <>
                <Field label="Default MCP action" value={form.mcpPolicyDefaultAction} onChange={(value) => set("mcpPolicyDefaultAction", value)} />
                <Field label="Allowed MCP servers" value={form.mcpPolicyAllowedServers} onChange={(value) => set("mcpPolicyAllowedServers", value)} placeholder="github, linear" />
              </>
            )}
          </FormSection>
        </div>

        {error && <p className="text-sm text-destructive">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>Cancel</Button>
          <Button disabled={saving || incomplete} onClick={() => void submit()}>
            {saving ? "Creating…" : "Create project"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function FormSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <fieldset className="grid gap-4 rounded-lg border p-4 sm:grid-cols-2">
      <legend className="px-1 text-sm font-medium">{title}</legend>
      {children}
    </fieldset>
  );
}

function ModelField({
  value,
  onChange,
  models,
  loading,
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  models: string[];
  loading: boolean;
  error: string | null;
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor="linear-create-model">Model *</Label>
      {models.length > 0 ? (
        <select
          id="linear-create-model"
          className={selectClassName}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          required
        >
          <option value="" disabled>Select a model…</option>
          {value && !models.includes(value) ? <option value={value}>{value}</option> : null}
          {models.map((model) => (
            <option key={model} value={model}>{model}</option>
          ))}
        </select>
      ) : (
        <Input
          id="linear-create-model"
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder="anthropic/claude-sonnet-4-6"
          required
        />
      )}
      <p className="text-xs text-muted-foreground" aria-live="polite">
        {loading
          ? "Loading provider models…"
          : error
            ? `Could not load provider models: ${error}`
            : models.length > 0
              ? `${models.length} models available`
              : "Enter a model ID"}
      </p>
    </div>
  );
}

function SelectField({
  id,
  label,
  value,
  onChange,
  options,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: { value: string; label: string }[];
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <select
        id={id}
        className={selectClassName}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>{option.label}</option>
        ))}
      </select>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  required,
  secret,
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  required?: boolean;
  secret?: boolean;
  placeholder?: string;
}) {
  const id = useId();
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}{required && " *"}</Label>
      <Input id={id} type={secret ? "password" : "text"} value={value} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} required={required} />
    </div>
  );
}

function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-3 sm:col-span-2">
      <Label>{label}</Label>
      <Switch checked={checked} onCheckedChange={onChange} />
    </div>
  );
}
