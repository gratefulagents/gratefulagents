import { useEffect, useMemo, useState } from "react";

import { client } from "@/lib/client";
import type { ModeTemplate } from "@/rpc/platform/service_pb";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

export function ModeTemplateSelect({
  id,
  value,
  enabled = true,
  onChange,
}: {
  id: string;
  value: string;
  enabled?: boolean;
  onChange: (value: string) => void;
}) {
  const [templates, setTemplates] = useState<ModeTemplate[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  useEffect(() => {
    if (!enabled) return;

    let active = true;

    async function loadTemplates() {
      setLoading(true);
      setLoadFailed(false);
      try {
        const response = await client.listModeTemplates({});
        if (!active) return;
        setTemplates(response.templates ?? []);
      } catch {
        if (!active) return;
        setTemplates([]);
        setLoadFailed(true);
      } finally {
        if (active) setLoading(false);
      }
    }

    void loadTemplates();
    return () => {
      active = false;
    };
  }, [enabled]);

  const options = useMemo(
    () =>
      templates.filter((template) => template.name !== "interactive").sort((left, right) => {
        const leftLabel = left.displayName || left.name;
        const rightLabel = right.displayName || right.name;
        return leftLabel.localeCompare(rightLabel);
      }),
    [templates],
  );
  const selectedTemplate = options.find((template) => template.name === value);
  const includesValue = value === "" || selectedTemplate !== undefined;

  return (
    <>
      <select
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className={selectClassName}
      >
        <option value="">Interactive (platform default)</option>
        {!includesValue ? <option value={value}>{value}</option> : null}
        {options.map((template) => (
          <option key={template.name} value={template.name}>
            {template.displayName || template.name}
            {template.displayName && template.displayName !== template.name ? ` (${template.name})` : ""}
          </option>
        ))}
      </select>
      <p className="text-[11px] leading-relaxed text-muted-foreground" aria-live="polite">
        {loading
          ? "Loading mode templates…"
          : loadFailed
            ? "Mode templates could not be loaded. The current selection is preserved."
            : selectedTemplate?.description ||
              "New runs use this mode unless the run explicitly chooses another one."}
      </p>
    </>
  );
}
