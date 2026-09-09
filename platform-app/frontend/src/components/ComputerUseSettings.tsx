import { useEffect, useState } from "react";
import { Monitor } from "lucide-react";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import {
  computerUsePermissions,
  openComputerUsePermission,
  type ComputerUsePermission,
  type ComputerUsePermissions,
} from "@/lib/computer-use";

export function ComputerUseSettings() {
  const [permissions, setPermissions] = useState<ComputerUsePermissions | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

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
    return () => {
      active = false;
      window.removeEventListener("focus", refresh);
    };
  }, []);

  async function openPermission(permission: ComputerUsePermission) {
    setBusy(true);
    setError("");
    try {
      await openComputerUsePermission(permission);
      setPermissions(await computerUsePermissions());
    } catch (cause) {
      setPermissions(null);
      setError(String(cause));
    } finally {
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
              Enable gratefulagents in macOS Privacy settings; you may need to add the app
              under Accessibility and restart it. You can revoke either permission there.
              These OS permissions are separate from consent to an individual session.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" disabled={busy}
                onClick={() => void openPermission("screen_recording")}>
                Screen Recording settings
              </Button>
              <Button variant="outline" size="sm" disabled={busy}
                onClick={() => void openPermission("accessibility")}>
                Accessibility settings
              </Button>
            </div>
          </>
        )}
      </div>
    </SettingsSection>
  );
}
