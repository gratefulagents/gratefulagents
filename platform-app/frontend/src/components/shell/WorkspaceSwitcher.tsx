import * as React from "react";
import { Check, ChevronsUpDown, Loader2, Plus, Server } from "lucide-react";

import { useAuth } from "@/contexts/AuthContext";
import { cn } from "@/lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { toast } from "@/components/ui/toaster";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

const inputClass = cn(
  "w-full h-[32px] px-2.5 rounded-[6px]",
  "bg-muted/60 ring-1 ring-inset ring-border/70",
  "text-[13px] outline-none",
  "focus:ring-[color:var(--color-primary)]/40",
);

/** Slack-style switcher between configured backend workspaces. */
export function WorkspaceSwitcher() {
  const { workspaces, activeWorkspaceId, switchWorkspace, addWorkspace } = useAuth();
  const [addOpen, setAddOpen] = React.useState(false);
  const [switching, setSwitching] = React.useState<string | null>(null);

  const active = workspaces.find((w) => w.id === activeWorkspaceId) ?? workspaces[0];

  async function handleSwitchWorkspace(id: string, isActive: boolean) {
    if (isActive || switching) return;
    setSwitching(id);
    try {
      await switchWorkspace(id);
    } catch {
      toast.error("Failed to switch workspace");
    } finally {
      setSwitching(null);
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          className={cn(
            "flex w-full items-center gap-2 rounded-[8px] px-2 py-1.5",
            "ring-1 ring-inset ring-border/70 bg-muted/40",
            "hover:bg-sidebar-accent transition-colors duration-[var(--dur-fast)]",
            "outline-none focus-visible:ring-2 focus-visible:ring-[color:var(--color-primary)]/50",
          )}
          title="Switch workspace"
        >
          <div className="flex size-[24px] shrink-0 items-center justify-center rounded-[6px] bg-[color:var(--color-primary)]/15 text-[color:var(--color-primary)]">
            <Server className="size-[14px]" />
          </div>
          <div className="min-w-0 flex-1 text-left">
            <div className="truncate text-[12.5px] font-medium tracking-tight">
              {active?.name || "Add workspace"}
            </div>
            {active?.endpointUrl && (
              <div className="truncate text-[10.5px] text-muted-foreground/80 font-mono">
                {active.endpointUrl}
              </div>
            )}
          </div>
          <ChevronsUpDown className="size-[14px] shrink-0 text-muted-foreground" />
        </DropdownMenuTrigger>

        <DropdownMenuContent align="start" className="w-[--anchor-width] min-w-[240px]">
          <DropdownMenuLabel className="text-[10.5px] tracking-[0.08em] uppercase text-muted-foreground/70">
            Workspaces
          </DropdownMenuLabel>
          {workspaces.map((ws) => {
            const isActive = ws.id === activeWorkspaceId;
            return (
              <DropdownMenuItem
                key={ws.id}
                onClick={(event) => {
                  if (!isActive) event.preventDefault();
                  void handleSwitchWorkspace(ws.id, isActive);
                }}
                disabled={!!switching && switching !== ws.id}
                className="gap-2"
              >
                <div className="flex size-[22px] shrink-0 items-center justify-center rounded-[6px] bg-[color:var(--color-primary)]/12 text-[color:var(--color-primary)]">
                  <Server className="size-[13px]" />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[12.5px]">{ws.name}</div>
                  {ws.endpointUrl && (
                    <div className="truncate text-[10.5px] text-muted-foreground/80 font-mono">
                      {ws.endpointUrl}
                    </div>
                  )}
                </div>
                {switching === ws.id ? (
                  <Loader2 className="size-[14px] shrink-0 animate-spin text-[color:var(--color-primary)] motion-reduce:animate-none" />
                ) : isActive ? (
                  <Check className="size-[14px] shrink-0 text-[color:var(--color-primary)]" />
                ) : null}
              </DropdownMenuItem>
            );
          })}
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setAddOpen(true)} disabled={!!switching} className="gap-2">
            <div className="flex size-[22px] shrink-0 items-center justify-center rounded-[6px] bg-muted">
              <Plus className="size-[13px]" />
            </div>
            <span className="text-[12.5px]">Add workspace</span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <AddWorkspaceDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        onAdd={async (env, name) => {
          await addWorkspace(env, name);
          setAddOpen(false);
        }}
      />
    </>
  );
}

function AddWorkspaceDialog({
  open,
  onOpenChange,
  onAdd,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdd: (
    env: { endpointUrl: string; cfAccessClientId: string; cfAccessClientSecret: string },
    name: string,
  ) => Promise<void>;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {/* Mounted only while open, so form state resets on every open. */}
        {open && <AddWorkspaceForm onCancel={() => onOpenChange(false)} onAdd={onAdd} />}
      </DialogContent>
    </Dialog>
  );
}

function AddWorkspaceForm({
  onCancel,
  onAdd,
}: {
  onCancel: () => void;
  onAdd: (
    env: { endpointUrl: string; cfAccessClientId: string; cfAccessClientSecret: string },
    name: string,
  ) => Promise<void>;
}) {
  const [name, setName] = React.useState("");
  const [endpointUrl, setEndpointUrl] = React.useState("");
  const [cfClientId, setCfClientId] = React.useState("");
  const [cfClientSecret, setCfClientSecret] = React.useState("");
  const [showCf, setShowCf] = React.useState(false);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const submit = async () => {
    const url = endpointUrl.trim();
    if (!url) {
      setError("gratefulagents URL is required");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await onAdd(
        {
          endpointUrl: url,
          cfAccessClientId: cfClientId.trim(),
          cfAccessClientSecret: cfClientSecret.trim(),
        },
        name.trim(),
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to add workspace");
      setBusy(false);
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle>Add workspace</DialogTitle>
          <DialogDescription>
            Connect to another gratefulagents backend. Each workspace keeps its own sign-in.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-[12px] text-muted-foreground">Name (optional)</label>
            <input
              className={inputClass}
              placeholder="Production"
              value={name}
              onChange={(e) => setName(e.target.value)}
              disabled={busy}
            />
          </div>
          <div>
            <label className="mb-1 block text-[12px] text-muted-foreground">gratefulagents URL</label>
            <input
              type="url"
              className={cn(inputClass, "font-mono")}
              placeholder="https://gratefulagents.example.com"
              value={endpointUrl}
              onChange={(e) => setEndpointUrl(e.target.value)}
              disabled={busy}
              autoFocus
            />
          </div>

          <button
            type="button"
            onClick={() => setShowCf((p) => !p)}
            className="text-[12px] text-muted-foreground transition-colors hover:text-foreground"
          >
            {showCf ? "Hide" : "Show"} Cloudflare Access
          </button>

          {showCf && (
            <div className="space-y-2">
              <input
                className={cn(inputClass, "font-mono")}
                placeholder="CF-Access-Client-Id"
                value={cfClientId}
                onChange={(e) => setCfClientId(e.target.value)}
                disabled={busy}
              />
              <input
                type="password"
                className={cn(inputClass, "font-mono")}
                placeholder="CF-Access-Client-Secret"
                value={cfClientSecret}
                onChange={(e) => setCfClientSecret(e.target.value)}
                disabled={busy}
              />
            </div>
          )}

          {error && (
            <p className="text-[12.5px] text-destructive">{error}</p>
          )}
        </div>

        <DialogFooter>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className={cn(
              "h-[32px] px-3 rounded-[6px] text-[12.5px] font-medium",
              "bg-muted/60 ring-1 ring-inset ring-border/70 hover:bg-muted disabled:opacity-50",
            )}
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={busy}
            className={cn(
              "h-[32px] px-3 rounded-[6px] text-[12.5px] font-medium",
              "bg-[color:var(--color-primary)] text-[color:var(--color-primary-foreground)]",
              "hover:brightness-110 active:brightness-95 disabled:opacity-50",
            )}
          >
            {busy ? "Adding..." : "Add"}
          </button>
        </DialogFooter>
    </>
  );
}
