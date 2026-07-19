import { useEffect, useState } from "react";

import { client } from "@/lib/client";
import { Switch } from "@/components/ui/switch";

interface SkillRow {
  name: string;
  version: string;
  description: string;
  resolvedDescription: string;
  mcpServerRefs: string[];
}

// SkillPicker renders toggle rows for the caller's Skills (reusable agent
// instructions) so forms can attach them to a project or agent. Selected names
// that no longer exist stay listed (flagged) so saves don't silently drop them.
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
      {skills.map((skill) => (
        <div key={skill.name} className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="text-[12.5px] font-medium">
              {skill.name}
              {skill.version && (
                <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">v{skill.version}</span>
              )}
              {(skill.mcpServerRefs ?? []).length > 0 && (
                <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">
                  brings: {skill.mcpServerRefs.join(", ")}
                </span>
              )}
            </div>
            {(skill.description || skill.resolvedDescription) && (
              <p className="text-[12px] text-muted-foreground">
                {skill.description || skill.resolvedDescription}
              </p>
            )}
          </div>
          <Switch
            aria-label={`Attach ${skill.name}`}
            checked={selected.includes(skill.name)}
            disabled={disabled}
            onCheckedChange={(on) => toggle(skill.name, on)}
          />
        </div>
      ))}
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
