import { useEffect, useState } from "react";

import { client } from "@/lib/client";
import { Switch } from "@/components/ui/switch";

interface ServerInfo {
  name: string;
  version: string;
  description: string;
}

// MCPServerPicker renders toggle rows for the caller's MCPServers (MCP server
// configs) so forms can attach them to a project or agent. Selected names
// that no longer exist stay listed (flagged) so saves don't silently drop them.
export function MCPServerPicker({
  selected,
  onChange,
  disabled,
}: {
  selected: string[];
  onChange: (names: string[]) => void;
  disabled?: boolean;
}) {
  const [servers, setServers] = useState<ServerInfo[]>([]);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const resp = await client.listMCPServers({});
        if (active) setServers(((resp.servers ?? []) as unknown as ServerInfo[]));
      } catch {
        if (active) setServers([]);
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

  const missing = selected.filter((name) => !servers.some((s) => s.name === name));

  if (loaded && servers.length === 0 && missing.length === 0) {
    return (
      <p className="text-[12px] text-muted-foreground">
        No MCP servers in your namespace — create one under Resources → MCP servers.
      </p>
    );
  }

  return (
    <div className="space-y-2.5">
      {servers.map((server) => (
        <div key={server.name} className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="text-[12.5px] font-medium">
              {server.name}
              {server.version && (
                <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">v{server.version}</span>
              )}
            </div>
            {server.description && <p className="text-[12px] text-muted-foreground">{server.description}</p>}
          </div>
          <Switch
            aria-label={`Attach ${server.name}`}
            checked={selected.includes(server.name)}
            disabled={disabled}
            onCheckedChange={(on) => toggle(server.name, on)}
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
