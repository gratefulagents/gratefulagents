import { useEffect, useState } from "react";
import { Sparkles } from "lucide-react";
import { client } from "@/lib/client";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { toast } from "@/components/ui/toaster";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

const SOUL_PLACEHOLDER = `# SOUL.md — your agent persona

Describe how *your* agent thinks so teammates can ask what you'd say.

## Identity
Who you are and the role you play on the team.

## What I care about
The things you always check for (e.g. tests, simplicity, security).

## How I review
How you react to a plan, design, or diff — what you push back on.
`;

// SoulSection lets a logged-in user edit their personal SOUL: a role/persona
// definition for their own agent. Other users' agents can consult it via the
// ask_teammate tool to get this user's likely perspective on a question, plan,
// or diff.
export function SoulSection() {
  const [content, setContent] = useState("");
  const [updatedAt, setUpdatedAt] = useState<Timestamp | undefined>(undefined);

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const s = await client.getMySoul({});
        setContent(s.content);
        setUpdatedAt(s.updatedAt);
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to load SOUL");
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  async function save() {
    setSaving(true);
    setStatus(null);
    setError(null);
    try {
      const s = await client.updateMySoul({ content: content.trim() });
      setContent(s.content);
      setUpdatedAt(s.updatedAt);
      setStatus("SOUL saved");
      toast.success("SOUL saved");
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to save SOUL";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsSection
      icon={<Sparkles />}
      title="SOUL"
      description="Your personal agent persona. Teammates can ask your agent for your perspective on a question, plan, or diff — it answers in your voice using what you write here."
    >
      <div className="space-y-2">
        <Textarea
          value={content}
          onChange={(e) => setContent(e.target.value)}
          disabled={loading}
          placeholder={SOUL_PLACEHOLDER}
          className="min-h-[260px] resize-y font-mono text-xs leading-relaxed"
          aria-label="SOUL content"
        />
        <p className="text-[11px] text-muted-foreground" aria-live="polite">
          {loading
            ? "Loading…"
            : lastSavedLabel(updatedAt)}
        </p>
      </div>

      <div className="flex items-center gap-3">
        <Button size="sm" onClick={() => void save()} disabled={saving || loading}>
          {saving ? "Saving…" : "Save SOUL"}
        </Button>
        {status && <span className="text-[12px] text-muted-foreground">{status}</span>}
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>
    </SettingsSection>
  );
}

// lastSavedLabel renders a friendly "last saved" line, or a hint to write one
// when the user has never saved a SOUL.
function lastSavedLabel(updatedAt: Timestamp | undefined): string {
  if (!updatedAt) {
    return "Not saved yet — anyone on your team can ask your agent once you save a SOUL.";
  }
  const millis = Number(updatedAt.seconds) * 1000;
  if (!Number.isFinite(millis) || millis <= 0) {
    return "Saved.";
  }
  return `Last saved ${new Date(millis).toLocaleString()}`;
}
