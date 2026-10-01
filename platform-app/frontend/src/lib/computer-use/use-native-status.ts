import { useCallback, useEffect, useRef, useState } from "react";
import { nativeStatus, relaunchApp, requestPermission, type NativeStatus, type Permission } from "./native";

/** Native permission/display status, re-polled every 2 s while `visible()` and the window is shown. */
export function useNativeStatus(visible: () => boolean = () => true) {
  const [status, setStatus] = useState<NativeStatus | null>(null);
  const [error, setError] = useState("");
  const visibleRef = useRef(visible);
  useEffect(() => {
    visibleRef.current = visible;
  });

  const refresh = useCallback(() => nativeStatus().then((next) => {
    setStatus(next);
    setError("");
  }, (cause: unknown) => {
    setError(cause instanceof Error ? cause.message : String(cause));
  }), []);

  useEffect(() => {
    void refresh();
    // System Settings does not reliably focus the webview when closed, so poll.
    const timer = setInterval(() => {
      if (!document.hidden && visibleRef.current()) void refresh();
    }, 2000);
    const onFocus = () => void refresh();
    window.addEventListener("focus", onFocus);
    return () => {
      clearInterval(timer);
      window.removeEventListener("focus", onFocus);
    };
  }, [refresh]);

  return { status, error, refresh };
}

/** Grant handler shared by setup and settings; remembers a Screen Recording request for the relaunch hint. */
export function usePermissionGrant(refresh: () => Promise<void>) {
  const [requestedScreen, setRequestedScreen] = useState(false);
  const [error, setError] = useState("");
  const grant = async (permission: Permission) => {
    setError("");
    try {
      await requestPermission(permission);
      if (permission === "screen_recording") setRequestedScreen(true);
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };
  const relaunch = () => relaunchApp().catch((cause: unknown) => setError(String(cause)));
  return { grant, relaunch, requestedScreen, error };
}
