import { useEffect, useState } from "react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { Switch } from "@/components/ui/switch";

interface SkillRow {
  name: string;
  version: string;
  description: string;
  resolvedDescription: string;
  mcpServerRefs: string[];
  phase: string;
  statusMessage: string;
}

function skillPhaseTone(phase: string): StatusTone {
  switch (phase) {
    case "Error":
      return "danger";
    case "Invalid":
      return "warning";
    default:
      return "neutral";
  }
}

// SkillPicker renders toggle rows for the caller's Skills (reusable agent
// instructions) so forms can attach them to a project or agent. Selected names
// that no longer exist stay listed (flagged) so saves don't silently drop them,
// and skills whose source failed to resolve show their phase and error so an
// attached skill that will load nothing is visible before the run starts.
export function SkillPicker({
  selected,
  onChange,
  disabled,
}: {
  selected: string[];
  onChange: (names: string[]) => void;
  disabled?: boolean;
}) {
  const [skills, setSkills] = useState<SkillRow[]>([]);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const resp = await client.listSkills({});
        if (active) setSkills(((resp.skills ?? []) as unknown as SkillRow[]));
      } catch {
        if (active) setSkills([]);
      } finally {
        if (active) setLoaded(true);
      }
    })();
    return () => {
      active = false;
    };
  }, []);

  function toggle(name: string, on: boolean) {
    const without = selected.filter((n) => n !== name);
    onChange(on ? [...without, name] : without);
  }

  const missing = selected.filter((name) => !skills.some((s) => s.name === name));

  if (loaded && skills.length === 0 && missing.length === 0) {
    return (
      <p className="text-[12px] text-muted-foreground">
        No skills in your namespace — create one under Resources → Skills.
      </p>
    );
  }

  return (
    <div className="space-y-2.5">
      {skills.map((skill) => {
        const phase = (skill.phase ?? "").trim();
        const unhealthy = phase === "Error" || phase === "Invalid";
        return (
          <div key={skill.name} className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[12.5px] font-medium">
                <span>{skill.name}</span>
                {skill.version && (
                  <span className="text-[11px] font-normal text-muted-foreground">v{skill.version}</span>
                )}
                {phase && phase !== "Ready" && (
                  <span
                    className={cn(
                      "inline-flex h-[16px] items-center rounded-full px-1.5 text-[10px] font-medium",
                      toneSoft[skillPhaseTone(phase)],
                    )}
                  >
                    {phase}
                  </span>
                )}
                {(skill.mcpServerRefs ?? []).length > 0 && (
                  <span className="text-[11px] font-normal text-muted-foreground">
                    brings: {skill.mcpServerRefs.join(", ")}
                  </span>
                )}
              </div>
              {(skill.description || skill.resolvedDescription) && (
                <p className="text-[12px] text-muted-foreground">
                  {skill.description || skill.resolvedDescription}
                </p>
              )}
              {unhealthy && skill.statusMessage && (
                <p className="text-[11.5px] text-destructive">{skill.statusMessage}</p>
              )}
            </div>
            <Switch
              aria-label={`Attach ${skill.name}`}
              checked={selected.includes(skill.name)}
              disabled={disabled}
              onCheckedChange={(on) => toggle(skill.name, on)}
            />
          </div>
        );
      })}
      {missing.map((name) => (
        <div key={name} className="flex items-center justify-between gap-3">
          <div className="text-[12.5px] font-medium">
            {name}
            <span className="ml-1.5 text-[11px] font-normal text-amber-600">not found in your namespace</span>
          </div>
          <Switch aria-label={`Detach ${name}`} checked disabled={disabled} onCheckedChange={(on) => toggle(name, on)} />
        </div>
      ))}
    </div>
  );
}
