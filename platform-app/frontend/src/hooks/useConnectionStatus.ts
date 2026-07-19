import * as React from "react";

export type ConnectionStatus = "online" | "offline" | "reconnecting";

/**
 * Reports the app's connectivity state. Uses `navigator.onLine` as a
 * coarse signal, and flips to `reconnecting` for ~2s after we come back
 * online so the banner can reassure the user that we noticed.
 */
export function useConnectionStatus(): ConnectionStatus {
  const initial: ConnectionStatus =
    typeof navigator !== "undefined" && !navigator.onLine ? "offline" : "online";
  const [status, setStatus] = React.useState<ConnectionStatus>(initial);

  React.useEffect(() => {
    let recoverTimer: number | undefined;

    function goOffline() {
      setStatus("offline");
    }
    function goOnline() {
      setStatus("reconnecting");
      window.clearTimeout(recoverTimer);
      recoverTimer = window.setTimeout(() => setStatus("online"), 2000);
    }
    window.addEventListener("offline", goOffline);
    window.addEventListener("online", goOnline);
    return () => {
      window.removeEventListener("offline", goOffline);
      window.removeEventListener("online", goOnline);
      window.clearTimeout(recoverTimer);
    };
  }, []);

  return status;
}
