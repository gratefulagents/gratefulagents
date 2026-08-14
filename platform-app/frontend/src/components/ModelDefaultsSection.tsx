import { useEffect, useState } from "react";
import { SlidersHorizontal } from "lucide-react";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import { PROVIDERS } from "@/components/create-flow/providers";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toaster";
import { client } from "@/lib/client";
import { REASONING_LEVELS } from "@/lib/reasoning";
import type { ModelDefaults } from "@/rpc/platform/service_pb";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

export function ModelDefaultsSection() {
  const [provider, setProvider] = useState("anthropic");
  const [model, setModel] = useState("");
  const [reasoningLevel, setReasoningLevel] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<Timestamp | undefined>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function applyServer(defaults: ModelDefaults) {
    setProvider(defaults.provider || "anthropic");
    setModel(defaults.model);
    setReasoningLevel(defaults.reasoningLevel);
    setEnabled(!defaults.disabled);
    setUpdatedAt(defaults.updatedAt);
  }

  useEffect(() => {
    let active = true;
    client.getMyModelDefaults({}).then(
      (defaults) => {
        if (!active) return;
        applyServer(defaults);
        setLoading(false);
      },
      (err: unknown) => {
        if (!active) return;
        setError(err instanceof Error ? err.message : "Failed to load model defaults");
        setLoading(false);
      },
    );
    return () => {
      active = false;
    };
  }, []);

  async function save(request: {
    provider: string;
    model: string;
    reasoningLevel: string;
    disabled: boolean;
  }) {
    setSaving(true);
    setError(null);
    try {
      applyServer(await client.updateMyModelDefaults(request));
      toast.success("Default model saved");
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to save model defaults";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsSection
      icon={<SlidersHorizontal />}
      title="Default model"
      description="Your personal default provider, model, and reasoning level. New projects, runs, chats, and scans start from these values; every form stays editable, so you can skip or override them anywhere."
    >
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="space-y-1.5">
          <Label htmlFor="model-defaults-provider">Provider</Label>
          <select
            id="model-defaults-provider"
            className={selectClassName}
            value={provider}
            onChange={(event) => setProvider(event.target.value)}
            disabled={loading}
          >
            {PROVIDERS.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="model-defaults-model">Model</Label>
          <Input
            id="model-defaults-model"
            value={model}
            onChange={(event) => setModel(event.target.value)}
            placeholder="provider default"
            disabled={loading}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="model-defaults-reasoning">Reasoning level</Label>
          <select
            id="model-defaults-reasoning"
            className={selectClassName}
            value={reasoningLevel}
            onChange={(event) => setReasoningLevel(event.target.value)}
            disabled={loading}
          >
            {REASONING_LEVELS.map((level) => (
              <option key={level || "default"} value={level}>
                {level || "default"}
              </option>
            ))}
          </select>
        </div>
      </div>

      <Label className="flex w-fit cursor-pointer items-center gap-2 text-[12px] font-normal">
        <Switch checked={enabled} onCheckedChange={setEnabled} disabled={loading} />
        Apply to new projects, runs, and scans
      </Label>

      <p className="text-[11px] text-muted-foreground" aria-live="polite">
        {loading ? "Loading…" : savedLabel(updatedAt)}
      </p>

      <div className="flex items-center gap-3">
        <Button size="sm" disabled={saving || loading} onClick={() => void save({
          provider,
          model: model.trim(),
          reasoningLevel,
          disabled: !enabled,
        })}>
          {saving ? "Saving…" : "Save default model"}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={saving || loading}
          onClick={() =>
            void save({ provider: "", model: "", reasoningLevel: "", disabled: false })
          }
        >
          Clear
        </Button>
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>
    </SettingsSection>
  );
}

function savedLabel(updatedAt: Timestamp | undefined) {
  if (!updatedAt) return "No default model saved; new resources start from the platform default.";
  const millis = Number(updatedAt.seconds) * 1000;
  if (!Number.isFinite(millis) || millis <= 0) return "Saved.";
  return `Last saved ${new Date(millis).toLocaleString()}`;
}
