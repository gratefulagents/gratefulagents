import { useEffect, useState } from "react";
import {
  Accessibility,
  AlertTriangle,
  CheckCircle2,
  Circle,
  ExternalLink,
  Monitor,
  RotateCcw,
} from "lucide-react";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import { ApprovalModeControl } from "@/components/ComputerUseApprovalMode";
import {
  computerUsePermissions,
  openComputerUsePermission,
  relaunchComputerUse,
  type ComputerUsePermission,
  type ComputerUsePermissions,
} from "@/lib/computer-use";
import { useComputerUseApprovalMode } from "@/lib/computer-use-preferences";
import { toneSoft } from "@/lib/status";
import { cn } from "@/lib/utils";

export function ComputerUseSettings() {
  const [permissions, setPermissions] = useState<ComputerUsePermissions | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [requested, setRequested] = useState(false);
  const mode = useComputerUseApprovalMode();

  useEffect(() => {
    let active = true;
    const refresh = () => {
      void computerUsePermissions().then((status) => {
        if (active) {
          setPermissions(status);
          setError("");
        }
      }).catch((cause: unknown) => {
        if (active) {
          setPermissions(null);
          setError(String(cause));
        }
      });
    };
    refresh();
    window.addEventListener("focus", refresh);
    // The webview does not always receive a focus event when System Settings
    // closes; poll while this section is visible so grants show without a click.
    const timer = setInterval(refresh, 2000);
    return () => {
      active = false;
      window.removeEventListener("focus", refresh);
      clearInterval(timer);
    };
  }, []);

  async function openPermission(permission: ComputerUsePermission) {
    setBusy(true);
    setError("");
    try {
      await openComputerUsePermission(permission);
      setRequested(true);
      setPermissions(await computerUsePermissions());
    } catch (cause) {
      setPermissions(null);
      setError(String(cause));
    } finally {
      setBusy(false);
    }
  }

  async function relaunch() {
    setBusy(true);
    setError("");
    try {
      await relaunchComputerUse();
    } catch (cause) {
      setError(String(cause));
      setBusy(false);
    }
  }

  const missing = !!permissions?.supported && !permissions.accessibility;

  return (
    <SettingsSection
      icon={<Monitor />}
      title="Computer use"
      description="Let an agent observe and act in the current approved Mac window while you supervise."
    >
      <div className="space-y-4 text-sm">
        <p className="text-[12px] leading-relaxed text-muted-foreground">
          Granting Accessibility permission does not capture your screen, send screen content, or
          authorize an agent to control your Mac. Supervised sessions start only from a run
          you own, and in the default Manual mode every action still needs your explicit approval.
        </p>

        {error && (
          <p role="alert" className={cn("rounded-md px-3 py-2 text-[12px]", toneSoft.danger)}>
            Could not check or update permissions: {error}
          </p>
        )}
        {!permissions && !error && <p role="status" className="text-[12px] text-muted-foreground">Checking permissions…</p>}
        {permissions && !permissions.supported && (
          <p className="text-[12px] text-muted-foreground">
            Computer use requires the macOS desktop app on macOS 15.2 or later; it is unavailable on this platform. The rest of the app is unchanged.
          </p>
        )}

        {permissions?.supported && (
          <div className="space-y-3">
            <h3 className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">macOS permissions</h3>
            <dl className="grid gap-2">
              <PermissionRow
                icon={<Accessibility />}
                name="Accessibility"
                detail="Allows clicks, scrolling, and typing."
                granted={permissions.accessibility}
                disabled={busy}
                onOpen={() => void openPermission("accessibility")}
              />
              <PermissionRow
                icon={<Monitor />}
                name="Screen Recording"
                detail="Optional: required only for Agent chooses windows, to discover window names and capture the selected window."
                granted={permissions.agentScreenRecording === true}
                disabled={busy}
                onOpen={() => void openPermission("agent_screen_recording")}
              />
            </dl>
            <p className="text-[11.5px] leading-relaxed text-muted-foreground">
              Selected window only uses the native macOS sharing picker and does not require broad Screen Recording permission.
              Agent chooses windows requires the separate Screen Recording permission above. Granting it does not start a session
              or authorize control: choose that mode and give sharing consent in a run's Computer use panel.
              After enabling it in System Settings, relaunch if macOS requests it, then reconnect.
              Stop sharing from macOS or the supervisor to revoke session access. OS permissions can be revoked in System Settings.
            </p>
            {missing && (
              <p className={cn("rounded-md px-3 py-2 text-[11.5px] leading-relaxed", toneSoft.warning)} role="note">
                If Accessibility is
                enabled in System Settings but still shows Not granted here, macOS is holding the grant for a
                different build of the app (development and unsigned builds are re-signed every time they are
                built): remove gratefulagents from that list with −, click the button again to re-add it, enable it,
                then relaunch.
              </p>
            )}
            {(requested || missing) && (
              <Button variant="outline" size="sm" disabled={busy} onClick={() => void relaunch()}>
                <RotateCcw data-icon="inline-start" />
                Relaunch gratefulagents
              </Button>
            )}
          </div>
        )}

        {permissions?.supported && <div className="space-y-3 border-t pt-4">
          <div>
            <h3 className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">Approval mode</h3>
            <p className="mt-1 text-[11.5px] leading-relaxed text-muted-foreground">
              How much a supervised session asks before acting. You can also change this from the Computer use panel while a session is running.
            </p>
          </div>
          <ApprovalModeControl />
          {mode === "auto" && (
            <p className={cn("flex items-start gap-1.5 rounded-md px-3 py-2 text-[11.5px] leading-relaxed", toneSoft.warning)} role="note">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>
                Skip all approvals is on: the agent can send, submit, delete, or purchase on your behalf in the approved window,
                and on-screen instructions may steer it. Only use with non-sensitive windows and stay at the keyboard.
              </span>
            </p>
          )}
          <p className="text-[11px] text-muted-foreground">
            Stored on this Mac only. Emergency stop: Control+Option+Command+Escape or the native tray.
          </p>
        </div>}
      </div>

    </SettingsSection>
  );
}

function PermissionRow({ icon, name, detail, granted, disabled, onOpen }: {
  icon: React.ReactNode;
  name: string;
  detail: string;
  granted: boolean;
  disabled: boolean;
  onOpen: () => void;
}) {
  return (
    <div className="flex items-start justify-between gap-3 rounded-lg border p-3">
      <div className="flex min-w-0 items-start gap-2.5">
        <span className="mt-0.5 grid size-6 shrink-0 place-items-center rounded-md bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/60 [&_svg]:size-3.5">
          {icon}
        </span>
        <div className="min-w-0">
          <dt className="text-[13px] font-medium">{name}</dt>
          <dd className="text-[11.5px] text-muted-foreground">{detail}</dd>
          <dd className={cn("mt-1.5 inline-flex h-5 items-center gap-1 rounded-full px-2 text-[11px] font-medium", granted ? toneSoft.success : toneSoft.neutral)}>
            {granted ? <CheckCircle2 className="size-3" /> : <Circle className="size-3" />}
            {granted ? "Granted" : "Not granted"}
          </dd>
        </div>
      </div>
      <Button variant="outline" size="sm" disabled={disabled} onClick={onOpen} aria-label={`${name} settings`}>
        <ExternalLink data-icon="inline-start" />
        Open
      </Button>
    </div>
  );
}
