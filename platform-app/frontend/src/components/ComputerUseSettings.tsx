import { useEffect, useState } from "react";
import { Monitor } from "lucide-react";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import {
  computerUsePermissions,
  openComputerUsePermission,
  relaunchComputerUse,
  type ComputerUsePermission,
  type ComputerUsePermissions,
} from "@/lib/computer-use";

export function ComputerUseSettings() {
  const [permissions, setPermissions] = useState<ComputerUsePermissions | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [requested, setRequested] = useState(false);

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

  return (
    <SettingsSection
      icon={<Monitor />}
      title="Computer use"
      description="macOS permission setup for supervised desktop sessions."
    >
      <div className="space-y-3 text-sm">
        <p className="text-muted-foreground">
          Granting these permissions does not capture your screen, send screen content, or
          authorize an agent to control your Mac. Supervised sessions start only from a run
          you own, and every action there still needs your explicit approval.
        </p>
        {error && <p role="alert">Could not check or update permissions: {error}</p>}
        {!permissions && !error && <p role="status">Checking permissions…</p>}
        {permissions && !permissions.supported && (
          <p>Permission setup requires the macOS desktop app; it is unavailable on this platform.</p>
        )}
        {permissions?.supported && (
          <>
            <dl className="grid grid-cols-2 gap-2">
              <dt>Screen Recording</dt>
              <dd>{permissions.screenRecording ? "Granted" : "Not granted"}</dd>
              <dt>Accessibility</dt>
              <dd>{permissions.accessibility ? "Granted" : "Not granted"}</dd>
            </dl>
            <p className="text-muted-foreground">
              Screen Recording allows screen capture. Accessibility allows input control.
              Each button below registers this app in the matching macOS Privacy list and opens it;
              turn gratefulagents on there. You can revoke either permission there at any time.
              These OS permissions are separate from consent to an individual session.
            </p>
            {(!permissions.screenRecording || !permissions.accessibility) && (
              <p className="text-muted-foreground" role="note">
                macOS applies a Screen Recording grant only after the app relaunches. If a permission is
                enabled in System Settings but still shows Not granted here, macOS is holding the grant for a
                different build of the app (development and unsigned builds are re-signed every time they are
                built): remove gratefulagents from that list with −, click the button again to re-add it, enable it,
                then relaunch.
              </p>
            )}
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" disabled={busy}
                onClick={() => void openPermission("screen_recording")}>
                Screen Recording settings
              </Button>
              <Button variant="outline" size="sm" disabled={busy}
                onClick={() => void openPermission("accessibility")}>
                Accessibility settings
              </Button>
              {(requested || !permissions.screenRecording || !permissions.accessibility) && (
                <Button variant="outline" size="sm" disabled={busy} onClick={() => void relaunch()}>
                  Relaunch gratefulagents
                </Button>
              )}
            </div>
          </>
        )}
      </div>
    </SettingsSection>
  );
}
