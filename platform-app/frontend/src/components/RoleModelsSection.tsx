import { useEffect, useState } from "react";
import { Bot } from "lucide-react";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "@/components/ui/toaster";
import { client } from "@/lib/client";
import type { RoleInstruction } from "@/rpc/platform/service_pb";

const providers = [
  { value: "openai", label: "OpenAI" },
  { value: "anthropic", label: "Anthropic" },
  { value: "copilot", label: "Copilot" },
] as const;

type Values = Record<string, string>;

function preferenceKey(roleName: string, provider: string) {
  return `${roleName}\u0000${provider}`;
}

function platformModel(role: RoleInstruction, provider: string) {
  return role.modelsByProvider[provider];
}

export function RoleModelsSection() {
  const [roles, setRoles] = useState<RoleInstruction[]>([]);
  const [values, setValues] = useState<Values>({});
  const [updatedAt, setUpdatedAt] = useState<Timestamp | undefined>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const [roleResponse, preferences] = await Promise.all([
          client.listRoleInstructions({}),
          client.getMyRoleModelPreferences({}),
        ]);
        setRoles(roleResponse.instructions);
        setValues(
          Object.fromEntries(
            preferences.preferences.map((preference) => [
              preferenceKey(preference.roleName, preference.provider),
              preference.model,
            ]),
          ),
        );
        setUpdatedAt(preferences.updatedAt);
      } catch (err) {
        setError(
          err instanceof Error ? err.message : "Failed to load role models",
        );
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const visibleProviders = new Set<string>(providers.map(({ value }) => value));
      const roleNames = new Set(roles.map((role) => role.name));
      const hiddenPreferences = Object.entries(values).flatMap(([key, rawModel]) => {
        const [roleName, provider] = key.split("\u0000");
        const model = rawModel.trim();
        return roleNames.has(roleName) && provider && !visibleProviders.has(provider) && model
          ? [{ roleName, provider, model }]
          : [];
      });
      const preferences: { roleName: string; provider: string; model: string }[] = [
        ...roles.flatMap((role) =>
          providers.flatMap(({ value: provider }) => {
            const model = (values[preferenceKey(role.name, provider)] || "").trim();
            return model ? [{ roleName: role.name, provider, model }] : [];
          }),
        ),
        ...hiddenPreferences,
      ];
      const saved = await client.updateMyRoleModelPreferences({ preferences });
      setValues(
        Object.fromEntries(
          saved.preferences.map((preference) => [
            preferenceKey(preference.roleName, preference.provider),
            preference.model,
          ]),
        ),
      );
      setUpdatedAt(saved.updatedAt);
      toast.success("Role models saved");
    } catch (err) {
      const message =
        err instanceof Error ? err.message : "Failed to save role models";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsSection
      icon={<Bot />}
      title="Role models"
      description="Choose a model for each specialist role and provider. Leave a field blank to use the platform mapping, or the parent model when that provider is not mapped. Saved choices apply to newly created runs."
    >
      <div className="space-y-4">
        {roles.map((role) => (
          <div key={role.name} className="surface-card space-y-3 p-4">
            <div>
              <h3 className="text-[13px] font-medium tracking-tight">
                {role.name}
              </h3>
              {role.description && (
                <p className="text-[11.5px] text-muted-foreground">
                  {role.description}
                </p>
              )}
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              {providers.map(({ value: provider, label }) => {
                const key = preferenceKey(role.name, provider);
                const defaultModel = platformModel(role, provider);
                const id = `role-model-${role.name}-${provider}`;
                return (
                  <div key={provider} className="space-y-1.5">
                    <Label htmlFor={id}>{label}</Label>
                    <Input
                      id={id}
                      value={values[key] || ""}
                      onChange={(event) =>
                        setValues((current) => ({
                          ...current,
                          [key]: event.target.value,
                        }))
                      }
                      disabled={loading}
                      placeholder={defaultModel || "Parent model"}
                      aria-describedby={`${id}-help`}
                    />
                    <p
                      id={`${id}-help`}
                      className="text-[11px] text-muted-foreground"
                    >
                      Platform: {defaultModel || "inherits parent model"}
                    </p>
                  </div>
                );
              })}
            </div>
          </div>
        ))}
        {!loading && roles.length === 0 && !error && (
          <p className="text-[12px] text-muted-foreground">
            No platform roles are available.
          </p>
        )}
      </div>

      <p className="text-[11px] text-muted-foreground" aria-live="polite">
        {loading ? "Loading…" : savedLabel(updatedAt)}
      </p>

      <div className="flex items-center gap-3">
        <Button
          size="sm"
          onClick={() => void save()}
          disabled={saving || loading}
        >
          {saving ? "Saving…" : "Save role models"}
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
  if (!updatedAt)
    return "No personal overrides saved; platform defaults are in use.";
  const millis = Number(updatedAt.seconds) * 1000;
  if (!Number.isFinite(millis) || millis <= 0) return "Saved.";
  return `Last saved ${new Date(millis).toLocaleString()}`;
}
