import * as React from "react";
import { useNavigate } from "react-router-dom";

import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { useAuth } from "@/contexts/AuthContext";
import { checkNativeAppVersion, type AppVersionMismatch } from "@/lib/app-version";
import { DISTRIBUTION_RELEASES_URL } from "@/lib/desktop-updater";
import { openExternal } from "@/lib/native";
import { platform } from "@/lib/platform";

/** Prompts packaged desktop and iOS clients when their server requires a newer app release. */
export function AppVersionPrompt() {
  const navigate = useNavigate();
  const { isAuthenticated } = useAuth();
  const [mismatch, setMismatch] = React.useState<AppVersionMismatch | null>(null);

  React.useEffect(() => {
    let cancelled = false;
    void checkNativeAppVersion()
      .then((result) => {
        if (!cancelled) setMismatch(result);
      })
      .catch(() => {
        // A version check must not turn a transient connection issue into a
        // second startup error; the normal connectivity UI handles it.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const update = async () => {
    const os = await platform();
    if (!isAuthenticated || os === "ios" || os === "android") {
      await openExternal(DISTRIBUTION_RELEASES_URL);
      return;
    }
    navigate("/settings/updates");
  };

  return (
    <ConfirmDialog
      open={mismatch !== null}
      onOpenChange={(open) => {
        if (!open) setMismatch(null);
      }}
      title="Update gratefulagents"
      description={
        mismatch ? (
          <span>
            This app is version <strong>v{mismatch.appVersion}</strong>, but the server requires{" "}
            <strong>v{mismatch.serverVersion}</strong>. Update the app to keep using this server
            reliably.
          </span>
        ) : undefined
      }
      confirmLabel="Update app"
      cancelLabel="Not now"
      onConfirm={update}
    />
  );
}
