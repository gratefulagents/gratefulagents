import * as React from "react";
import { DownloadCloud, RefreshCw } from "lucide-react";

import { SettingsSection } from "@/components/settings-section";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";
import { SegmentedControl } from "@/components/shell/SegmentedControl";
import { Button } from "@/components/ui/button";
import { BUILD_COMMIT, BUILD_COMMIT_SHORT } from "@/lib/build-info";
import {
  checkForDesktopUpdate,
  DEFAULT_UPDATE_CHECK_INTERVAL_HOURS,
  DISTRIBUTION_RELEASES_URL,
  getAppVersion,
  getUpdateCheckIntervalHours,
  installDesktopUpdate,
  restartApp,
  setUpdateCheckIntervalHours,
  UPDATE_CHECK_INTERVAL_OPTIONS,
  type UpdateCheckResult,
  type UpdateProgress,
} from "@/lib/desktop-updater";
import { openExternal } from "@/lib/native";
import { isTauri } from "@/lib/platform";

/** /settings/updates — credential-free desktop auto-update. */
export default function UpdatesPage() {
  return (
    <SettingsSubPage
      title="Desktop updates"
      description="Keep the desktop app up to date from public release builds."
    >
      {isTauri ? (
        <UpdateSettings />
      ) : (
        <SettingsSection
          icon={<DownloadCloud />}
          title="Desktop app only"
          description="The web app is always current — auto-update applies to the desktop app."
        >
          <p className="text-[12px] text-muted-foreground">
            Install the desktop app to manage updates here.
          </p>
        </SettingsSection>
      )}
    </SettingsSubPage>
  );
}

type Phase = "idle" | "checking" | "upToDate" | "available" | "installing" | "ready";

function UpdateSettings() {
  const [appVersion, setAppVersion] = React.useState<string | null>(null);
  const [phase, setPhase] = React.useState<Phase>("idle");
  const [result, setResult] = React.useState<UpdateCheckResult | null>(null);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  const [error, setError] = React.useState("");
  const [intervalHours, setIntervalHours] = React.useState<number | null>(null);

  React.useEffect(() => {
    void getAppVersion().then(setAppVersion);
    void getUpdateCheckIntervalHours().then(setIntervalHours);
  }, []);

  const handleIntervalChange = (value: string) => {
    const hours = Number(value);
    setIntervalHours(hours);
    void setUpdateCheckIntervalHours(hours);
  };

  const handleCheck = async () => {
    setError("");
    setResult(null);
    setPhase("checking");
    try {
      const check = await checkForDesktopUpdate();
      setResult(check);
      setPhase(check.available ? "available" : "upToDate");
    } catch (err) {
      setPhase("idle");
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const handleInstall = async () => {
    setError("");
    setPhase("installing");
    setProgress(null);
    try {
      await installDesktopUpdate(setProgress);
      setPhase("ready");
    } catch (err) {
      setPhase("available");
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <SettingsSection
      icon={<RefreshCw />}
      title="App updates"
      description="Check for a newer desktop build and install it in place."
      aside={
        <span className="font-mono text-[11px] text-muted-foreground">
          v{appVersion ?? "…"}
          {BUILD_COMMIT_SHORT && (
            <span title={`Build commit ${BUILD_COMMIT ?? ""}`}> · build {BUILD_COMMIT_SHORT}</span>
          )}
        </span>
      }
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            onClick={() => void handleCheck()}
            disabled={phase === "checking" || phase === "installing"}
          >
            {phase === "checking" ? "Checking…" : "Check for updates"}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => void openExternal(DISTRIBUTION_RELEASES_URL)}
          >
            Open releases page
          </Button>
        </div>

        {phase === "upToDate" && result && (
          <p className="text-[12.5px] text-muted-foreground">
            You’re on the latest build (v{result.currentVersion}).
          </p>
        )}

        {(phase === "available" || phase === "installing" || phase === "ready") && result && (
          <div className="space-y-2 rounded-lg border border-border bg-muted/30 p-3">
            <p className="text-[13px] font-medium">
              Update available: v{result.version}
              <span className="ml-2 font-normal text-muted-foreground">
                (current v{result.currentVersion})
              </span>
            </p>
            {result.notes && (
              <p className="max-w-[70ch] whitespace-pre-wrap text-[12px] text-muted-foreground">
                {result.notes}
              </p>
            )}
            {phase === "installing" ? (
              <ProgressLine progress={progress} />
            ) : phase === "ready" ? (
              <div className="flex items-center gap-2">
                <Button size="sm" onClick={() => void restartApp()}>
                  Restart now
                </Button>
                <span className="text-[12px] text-muted-foreground">
                  Update installed — restart to finish.
                </span>
              </div>
            ) : result.canAutoInstall ? (
              <Button size="sm" onClick={() => void handleInstall()}>
                Download &amp; install
              </Button>
            ) : (
              <p className="text-[12px] text-muted-foreground">
                {result.installHint ?? "Download the new build from the releases page."}
              </p>
            )}
          </div>
        )}

        {error && (
          <p role="alert" className="text-[12px] text-destructive">
            {error}
          </p>
        )}

        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-border/60 pt-3">
          <div>
            <p className="text-[13px] font-medium">Automatic checks</p>
            <p className="text-[12px] text-muted-foreground">
              How often to look for new builds while the app is running.
            </p>
          </div>
          <SegmentedControl
            size="sm"
            value={String(intervalHours ?? DEFAULT_UPDATE_CHECK_INTERVAL_HOURS)}
            onChange={handleIntervalChange}
            options={UPDATE_CHECK_INTERVAL_OPTIONS.map((option) => ({
              value: String(option.hours),
              label: option.label,
            }))}
          />
        </div>
      </div>
    </SettingsSection>
  );
}

function ProgressLine({ progress }: { progress: UpdateProgress | null }) {
  const downloaded = progress?.downloaded ?? 0;
  const total = progress?.total ?? null;
  const percent = total ? Math.min(100, Math.round((downloaded / total) * 100)) : null;
  const mb = (n: number) => (n / (1024 * 1024)).toFixed(1);
  return (
    <div className="space-y-1.5" role="status" aria-live="polite">
      <div className="h-1.5 w-full max-w-[360px] overflow-hidden rounded-full bg-muted">
        <div
          className="h-full rounded-full bg-primary transition-[width] duration-200"
          style={{ width: `${percent ?? 12}%` }}
        />
      </div>
      <p className="font-mono text-[11px] text-muted-foreground">
        {percent != null
          ? `${percent}% — ${mb(downloaded)} / ${mb(total ?? 0)} MB`
          : `${mb(downloaded)} MB downloaded…`}
      </p>
    </div>
  );
}
