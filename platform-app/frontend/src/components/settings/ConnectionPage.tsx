import * as React from "react";
import { Boxes, Server } from "lucide-react";

import { SettingsSection } from "@/components/settings-section";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "@/components/ui/toaster";
import { useAuth } from "@/contexts/AuthContext";
import type { AppEnvironment } from "@/lib/app-environment";
import { isTauri } from "@/lib/platform";
import { toneSoft } from "@/lib/status";
import { cn } from "@/lib/utils";

/**
 * /settings/connection — backend endpoint and workspaces for this device.
 * Only meaningful in the desktop (Tauri) app; the web app always talks to
 * the backend that serves it.
 */
export default function ConnectionPage() {
  const { connectToServer, environment, error, isConnected } = useAuth();
  const environmentKey = [
    environment.endpointUrl,
    environment.cfAccessClientId,
    environment.cfAccessClientSecret,
  ].join("\0");

  return (
    <SettingsSubPage
      title="Connection"
      description="Backend endpoint, Cloudflare Access, and workspaces on this device."
    >
      {isTauri ? (
        <>
          <BackendEndpointSettings
            key={environmentKey}
            connectToServer={connectToServer}
            environment={environment}
            error={error}
            isConnected={isConnected}
          />
          <WorkspacesSettings />
        </>
      ) : (
        <SettingsSection
          icon={<Server />}
          title="Desktop app only"
          description="The web app always connects to the backend that serves it. Endpoint and workspace switching are available in the desktop app."
        >
          <ConnectionPill connected={isConnected} />
        </SettingsSection>
      )}
    </SettingsSubPage>
  );
}

function ConnectionPill({ connected }: { connected: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-1.5 rounded-full pl-1.5 pr-2 text-[11px] font-medium whitespace-nowrap select-none",
        toneSoft[connected ? "success" : "neutral"],
      )}
    >
      <span className="size-1.5 rounded-full bg-current" />
      {connected ? "Connected" : "Disconnected"}
    </span>
  );
}

function BackendEndpointSettings({
  connectToServer,
  environment,
  error,
  isConnected,
}: {
  connectToServer: (environment: AppEnvironment) => Promise<void>;
  environment: AppEnvironment;
  error: string | null;
  isConnected: boolean;
}) {
  const [endpoint, setEndpoint] = React.useState(environment.endpointUrl);
  const [cfClientId, setCfClientId] = React.useState(environment.cfAccessClientId);
  const [cfClientSecret, setCfClientSecret] = React.useState(environment.cfAccessClientSecret);
  const [isSaving, setIsSaving] = React.useState(false);

  const save = async () => {
    setIsSaving(true);
    try {
      await connectToServer({
        endpointUrl: endpoint,
        cfAccessClientId: cfClientId,
        cfAccessClientSecret: cfClientSecret,
      });
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <SettingsSection
      icon={<Server />}
      title="Workspace backend"
      description="Endpoint and Cloudflare Access for the active workspace."
      aside={<ConnectionPill connected={isConnected} />}
    >
      <div className="space-y-3">
        <div className="space-y-1.5">
          <Label htmlFor="settings-endpoint" className="text-[12.5px]">
            Endpoint URL
          </Label>
          <Input
            id="settings-endpoint"
            className="font-mono text-[13px]"
            placeholder="http://localhost:8090"
            value={endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
          />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="settings-cf-id" className="text-[12.5px]">
              CF Access client ID
            </Label>
            <Input
              id="settings-cf-id"
              className="font-mono text-[13px]"
              placeholder="Optional"
              value={cfClientId}
              onChange={(e) => setCfClientId(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="settings-cf-secret" className="text-[12.5px]">
              CF Access client secret
            </Label>
            <Input
              id="settings-cf-secret"
              type="password"
              className="font-mono text-[13px]"
              placeholder="Optional"
              value={cfClientSecret}
              onChange={(e) => setCfClientSecret(e.target.value)}
            />
          </div>
        </div>
        <div className="flex items-center gap-3">
          <Button size="sm" disabled={isSaving} onClick={() => void save()}>
            {isSaving ? "Connecting…" : "Save & connect"}
          </Button>
          {error && (
            <p className="text-[12px] text-destructive" role="alert">
              {error}
            </p>
          )}
        </div>
      </div>
    </SettingsSection>
  );
}

function WorkspacesSettings() {
  const {
    workspaces,
    activeWorkspaceId,
    switchWorkspace,
    removeWorkspace,
    renameWorkspace,
  } = useAuth();
  const [pendingRemove, setPendingRemove] = React.useState<{ id: string; name: string } | null>(
    null,
  );

  return (
    <SettingsSection
      icon={<Boxes />}
      title="Workspaces"
      description="Switch between backends. Each workspace keeps its own sign-in; add one from the workspace menu in the sidebar."
    >
      <div className="space-y-2">
        {workspaces.map((ws) => {
          const isActive = ws.id === activeWorkspaceId;
          return (
            <div
              key={ws.id}
              className={cn(
                "flex items-center gap-2 rounded-[8px] p-2 pl-3",
                "ring-1 ring-inset transition-colors",
                isActive
                  ? "bg-[color:var(--color-primary)]/8 ring-[color:var(--color-primary)]/25"
                  : "bg-muted/40 ring-border/70",
              )}
            >
              <div className="min-w-0 flex-1">
                <input
                  className="w-full rounded-sm bg-transparent text-[13px] font-medium tracking-tight outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                  defaultValue={ws.name}
                  aria-label="Workspace name"
                  onBlur={(e) => {
                    const next = e.target.value.trim();
                    if (next && next !== ws.name) void renameWorkspace(ws.id, next);
                  }}
                />
                <div className="truncate font-mono text-[11px] text-muted-foreground/80">
                  {ws.endpointUrl || "same-origin"}
                </div>
              </div>
              {isActive ? (
                <span
                  className={cn(
                    "inline-flex h-5 items-center rounded-full px-2 text-[11px] font-medium select-none",
                    toneSoft.running,
                  )}
                >
                  Active
                </span>
              ) : (
                <Button variant="outline" size="sm" onClick={() => void switchWorkspace(ws.id)}>
                  Switch
                </Button>
              )}
              <Button
                variant="ghost"
                size="sm"
                className="text-muted-foreground hover:bg-destructive/15 hover:text-destructive"
                disabled={workspaces.length <= 1}
                title={
                  workspaces.length <= 1 ? "Can't remove the only workspace" : "Remove workspace"
                }
                onClick={() => setPendingRemove({ id: ws.id, name: ws.name })}
              >
                Remove
              </Button>
            </div>
          );
        })}
      </div>
      <ConfirmDialog
        open={pendingRemove != null}
        onOpenChange={(open) => !open && setPendingRemove(null)}
        title={`Remove “${pendingRemove?.name ?? ""}”?`}
        description="This forgets the workspace and its sign-in on this device. The backend itself is not touched."
        confirmLabel="Remove workspace"
        destructive
        onConfirm={async () => {
          if (!pendingRemove) return;
          await removeWorkspace(pendingRemove.id);
          toast.success("Workspace removed");
        }}
      />
    </SettingsSection>
  );
}
