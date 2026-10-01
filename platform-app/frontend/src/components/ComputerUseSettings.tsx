import { Accessibility, AlertTriangle, Monitor, ScreenShare } from "lucide-react";
import { SettingsSection } from "@/components/settings-section";
import { ApprovalModeToggle, PermissionRow, RelaunchHint } from "@/components/ComputerUsePanel";
import { setDefaultApprovalMode, useDefaultApprovalMode } from "@/lib/computer-use/preferences";
import { usePermissionGrant, useNativeStatus } from "@/lib/computer-use/use-native-status";
import { toneSoft } from "@/lib/status";
import { cn } from "@/lib/utils";

export function ComputerUseSettings() {
  const mode = useDefaultApprovalMode();
  const { status, error: statusError, refresh } = useNativeStatus();
  const { grant, relaunch, requestedScreen, error: grantError } = usePermissionGrant(refresh);
  const error = grantError || statusError;

  return (
    <SettingsSection
      icon={<Monitor />}
      title="Computer use"
      description="Let an agent see one display of this Mac and use the mouse and keyboard. Start it from a run's Computer tab; stop anytime with ⌃⌥⌘⎋."
    >
      <div className="space-y-4 text-sm">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <p className="text-[13px] font-medium">Default approval</p>
            <p className="text-[12px] text-muted-foreground">
              {mode === "ask"
                ? "Ask first: you allow each click, keystroke and URL. Screenshots never ask."
                : "Autonomous: actions run as soon as the agent asks. You can still pause or stop."}
            </p>
          </div>
          <ApprovalModeToggle value={mode} onChange={setDefaultApprovalMode} />
        </div>

        {error && (
          <p role="alert" className={cn("flex items-center gap-2 rounded-md px-3 py-2 text-[12px]", toneSoft.danger)}>
            <AlertTriangle className="size-3.5" />{error}
          </p>
        )}
        {!status && !error && <p role="status" className="text-[12px] text-muted-foreground">Checking permissions…</p>}
        {status && !status.supported && (
          <p className="text-[12px] text-muted-foreground">Computer use is available in the macOS desktop app.</p>
        )}
        {status?.supported && (
          <div>
            <h3 className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">macOS permissions</h3>
            <ul className="divide-y divide-border/60">
              <PermissionRow icon={<Accessibility />} title="Accessibility" detail="Lets the agent click, scroll and type."
                granted={status.accessibility} onGrant={() => void grant("accessibility")} />
              <PermissionRow icon={<ScreenShare />} title="Screen Recording" detail="Lets the agent see the display you choose."
                granted={status.screenRecording} onGrant={() => void grant("screen_recording")}>
                {requestedScreen && !status.screenRecording && <RelaunchHint onRelaunch={() => void relaunch()} />}
              </PermissionRow>
            </ul>
          </div>
        )}
      </div>
    </SettingsSection>
  );
}
