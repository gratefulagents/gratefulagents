import * as React from "react";
import { useNavigate } from "react-router-dom";

import { toast } from "@/components/ui/toaster";
import {
  checkForDesktopUpdate,
  getUpdateCheckIntervalHours,
  shouldAutoCheck,
  UPDATE_CHECK_INTERVAL_EVENT,
} from "@/lib/desktop-updater";

/**
 * Silent desktop update checks: once shortly after launch, then periodically
 * per the stored interval (Settings → Desktop updates; 0 = launch only,
 * changes reschedule live via UPDATE_CHECK_INTERVAL_EVENT). Runs only in the
 * desktop Tauri app and on CI-stamped builds. Public release assets require
 * no user credentials. On a hit it shows a toast that deep-links to
 * /settings/updates, where install + restart live — re-toasting only when a
 * version not yet announced shows up. Failures stay silent — the settings
 * page surfaces errors on manual checks.
 */
export function useDesktopUpdateCheck() {
  const navigate = useNavigate();
  const lastNotifiedVersion = React.useRef<string | null>(null);
  const inFlight = React.useRef(false);

  React.useEffect(() => {
    let cancelled = false;
    let intervalId: number | null = null;

    const runCheck = async () => {
      if (inFlight.current) return;
      inFlight.current = true;
      try {
        const enabled = await shouldAutoCheck();
        if (!enabled || cancelled) return;
        const result = await checkForDesktopUpdate();
        if (cancelled || !result.available || !result.version) return;
        if (result.version === lastNotifiedVersion.current) return;
        lastNotifiedVersion.current = result.version;
        toast("Update available", {
          id: "desktop-update",
          description: `gratefulagents v${result.version} is ready (you're on v${result.currentVersion}).`,
          duration: 15000,
          action: {
            label: "Review",
            onClick: () => navigate("/settings/updates"),
          },
        });
      } catch {
        // Silent: auto-check must never disturb the running app.
      } finally {
        inFlight.current = false;
      }
    };

    const reschedule = async () => {
      const hours = await getUpdateCheckIntervalHours();
      if (cancelled) return;
      if (intervalId != null) window.clearInterval(intervalId);
      intervalId =
        hours > 0 ? window.setInterval(() => void runCheck(), hours * 60 * 60 * 1000) : null;
    };

    // Small delay: keep startup IPC/network quiet while the shell loads.
    const timer = window.setTimeout(() => void runCheck(), 6000);
    void reschedule();
    const onIntervalChanged = () => void reschedule();
    window.addEventListener(UPDATE_CHECK_INTERVAL_EVENT, onIntervalChanged);

    return () => {
      cancelled = true;
      window.clearTimeout(timer);
      if (intervalId != null) window.clearInterval(intervalId);
      window.removeEventListener(UPDATE_CHECK_INTERVAL_EVENT, onIntervalChanged);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}
