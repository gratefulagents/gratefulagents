import { useEffect, useState } from "react";
import { GitCommitHorizontal } from "lucide-react";
import { client } from "@/lib/client";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "@/components/ui/toaster";
import { useAuth } from "@/contexts/AuthContext";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

// GitIdentitySection lets a logged-in user configure commit authorship.
export function GitIdentitySection() {
  const { user } = useAuth();

  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [updatedAt, setUpdatedAt] = useState<Timestamp | undefined>(undefined);

  const [loading, setLoading] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const identity = await client.getMyGitIdentity({});
        setName(identity.name);
        setEmail(identity.email);
        setUpdatedAt(identity.updatedAt);
        setLoaded(true);
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to load git settings");
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  const trimmedName = name.trim();
  const trimmedEmail = email.trim();
  // Server rule: both set together, or both empty (clears the identity).
  const incomplete =
    (trimmedName === "") !== (trimmedEmail === "");

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const identity = await client.updateMyGitIdentity({
        name: trimmedName,
        email: trimmedEmail,
      });
      setName(identity.name);
      setEmail(identity.email);
      setUpdatedAt(identity.updatedAt);
      toast.success("Git settings saved");
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to save git identity";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsSection
      icon={<GitCommitHorizontal />}
      title="Git settings"
      description="Configure commit authorship for new runs. Leave both fields empty to author commits as the gratefulagents GitHub App."
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor="git-identity-name">Name</Label>
          <Input
            id="git-identity-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={loading || !loaded}
            placeholder={user?.name || "Ada Lovelace"}
            autoComplete="name"
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="git-identity-email">Email</Label>
          <Input
            id="git-identity-email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            disabled={loading || !loaded}
            placeholder={user?.email || "you@example.com"}
            autoComplete="email"
          />
          <p className="text-[11px] text-muted-foreground">
            Use your GitHub noreply address (username@users.noreply.github.com) to keep your
            personal email out of public commits.
          </p>
        </div>
      </div>

      <p className="text-[11px] text-muted-foreground">
        The gratefulagents GitHub App is always credited with a Co-authored-by trailer.
      </p>

      <p className="text-[11px] text-muted-foreground" aria-live="polite">
        {loading ? "Loading…" : statusLabel(updatedAt, trimmedName, trimmedEmail)}
      </p>

      <div className="flex items-center gap-3">
        <Button
          size="sm"
          onClick={() => void save()}
          disabled={saving || loading || !loaded || incomplete}
        >
          {saving ? "Saving…" : "Save git settings"}
        </Button>
        {incomplete && (
          <span className="text-[12px] text-muted-foreground">
            Set both name and email, or clear both.
          </span>
        )}
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>
    </SettingsSection>
  );
}

function statusLabel(
  updatedAt: Timestamp | undefined,
  name: string,
  email: string,
): string {
  if (!updatedAt) {
    return "Not set — commits are currently authored by the agent identity.";
  }
  const preview = name && email ? `Commits will be authored as ${name} <${email}>. ` : "";
  const millis = Number(updatedAt.seconds) * 1000;
  if (!Number.isFinite(millis) || millis <= 0) {
    return `${preview}Saved.`;
  }
  return `${preview}Last saved ${new Date(millis).toLocaleString()}`;
}
