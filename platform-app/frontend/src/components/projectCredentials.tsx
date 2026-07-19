import { Check, Minus } from "lucide-react";

import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import type { Project } from "@/rpc/platform/service_pb";

function CredentialItem({ label, present }: { label: string; present: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 text-[12px] tracking-tight whitespace-nowrap",
        present ? "text-foreground" : "text-muted-foreground/60",
      )}
      title={`${label}: ${present ? "configured" : "missing"}`}
    >
      {present ? (
        <Check className={cn("size-3", toneText.success)} aria-hidden />
      ) : (
        <Minus className="size-3 text-muted-foreground/40" aria-hidden />
      )}
      {label}
      <span className="sr-only">{present ? "configured" : "missing"}</span>
    </span>
  );
}

/**
 * Inline credential status — quiet check/dash items (configured first)
 * instead of a row of badge pills.
 */
export function ProjectCredentialBadges({ project }: { project: Project }) {
  const status = project.credentialStatus;
  if (!status && project.providerKeys.length === 0) {
    return <span className="text-muted-foreground/50">—</span>;
  }

  const items: Array<{ label: string; present: boolean }> = [];
  if (status) {
    items.push(
      { label: "GitHub", present: status.githubTokenPresent },
      { label: "Anthropic", present: status.anthropicApiKeyPresent },
      { label: "OpenAI", present: status.openaiApiKeyPresent },
    );
  }
  for (const key of project.providerKeys) {
    items.push({ label: `${key.provider} secret`, present: true });
  }
  items.sort((a, b) => Number(b.present) - Number(a.present));

  return (
    <div className="flex flex-wrap items-center gap-x-3.5 gap-y-1">
      {items.map((item) => (
        <CredentialItem key={item.label} label={item.label} present={item.present} />
      ))}
    </div>
  );
}
