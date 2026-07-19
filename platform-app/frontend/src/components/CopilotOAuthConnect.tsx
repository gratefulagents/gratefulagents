import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Check, Clipboard, ExternalLink, GitBranch as GithubIcon, Loader2 } from "lucide-react";

import { client } from "@/lib/client";
import {
  pollCopilotOAuth,
  startCopilotOAuth,
  type CopilotOAuthStart,
} from "@/lib/copilot-oauth";
import { copyText, openExternal } from "@/lib/native";
import { isTauri } from "@/lib/platform";
import { toneSoft, toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";

interface SavedCredentials {
  namespace: string;
  anthropicApiKeyPresent: boolean;
  openaiApiKeyPresent: boolean;
  openrouterApiKeyPresent: boolean;
  xaiApiKeyPresent: boolean;
  anthropicOauthPresent: boolean;
  openaiOauthPresent: boolean;
  copilotOauthPresent: boolean;
  githubTokenPresent: boolean;
}

interface CopilotOAuthConnectProps {
  onSaved: (credentials: SavedCredentials) => void;
  className?: string;
}

type Phase = "idle" | "starting" | "pending" | "saving" | "done";

export function CopilotOAuthConnect({ onSaved, className }: CopilotOAuthConnectProps) {
  const [phase, setPhase] = useState<Phase>("idle");
  const [session, setSession] = useState<CopilotOAuthStart | null>(null);
  const [pollDelay, setPollDelay] = useState(5);
  const [login, setLogin] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const verificationUrl = useMemo(() => {
    if (!session) return "";
    return session.verificationUriComplete || session.verificationUri;
  }, [session]);

  const start = useCallback(async () => {
    setPhase("starting");
    setSession(null);
    setLogin(null);
    setCopied(false);
    setError(null);
    try {
      const next = await startCopilotOAuth();
      setSession(next);
      setPollDelay(Math.max(next.interval || 5, 5));
      setPhase("pending");
      void openExternal(next.verificationUriComplete || next.verificationUri);
    } catch (err) {
      setPhase("idle");
      setError(err instanceof Error ? err.message : "Failed to start Copilot login");
    }
  }, []);

  useEffect(() => {
    if (phase !== "pending" || !session) return;

    const activeSession = session;
    let cancelled = false;
    let timeout: number | undefined;

    async function tick() {
      try {
        const result = await pollCopilotOAuth(activeSession.deviceCode);
        if (cancelled) return;

        if (result.status === "completed") {
          if (!result.copilotOauthJson) {
            throw new Error("Copilot login completed without credentials");
          }
          setPhase("saving");
          setLogin(result.login ?? null);
          const updated = await client.updateMyCredentials({
            copilotOauthJson: result.copilotOauthJson,
          });
          if (!mountedRef.current) return;
          onSaved(updated);
          setPhase("done");
          return;
        }

        if (result.status === "pending") {
          const nextDelay = Math.max(result.interval ?? pollDelay, 5);
          setPollDelay(nextDelay);
          timeout = window.setTimeout(tick, nextDelay * 1000);
          return;
        }

        setPhase("idle");
        setError(result.error || "Copilot login did not complete");
      } catch (err) {
        if (cancelled) return;
        setPhase("idle");
        setError(err instanceof Error ? err.message : "Failed to complete Copilot login");
      }
    }

    timeout = window.setTimeout(tick, pollDelay * 1000);
    return () => {
      cancelled = true;
      if (timeout !== undefined) window.clearTimeout(timeout);
    };
  }, [onSaved, phase, pollDelay, session]);

  const copyCode = useCallback(async () => {
    if (!session) return;
    const ok = await copyText(session.userCode);
    setCopied(ok);
    if (ok) window.setTimeout(() => setCopied(false), 1800);
  }, [session]);

  if (!isTauri) return null;

  const busy = phase === "starting" || phase === "pending" || phase === "saving";

  return (
    <div className={cn("rounded-lg border bg-muted/20 p-3", className)}>
      <div className="flex items-start gap-3">
        <span className={cn("mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg", toneSoft.neutral)}>
          <GithubIcon className="size-4" />
        </span>
        <div className="min-w-0 flex-1 space-y-3">
          <div className="space-y-1">
            <h3 className="text-sm font-medium">GitHub Copilot</h3>
            <p className="text-xs text-muted-foreground">
              Connect with GitHub, then gratefulagents stores refreshable Copilot credentials for new projects.
            </p>
          </div>

          {session && phase !== "done" ? (
            <div className="space-y-2 rounded-md border bg-background/70 p-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs text-muted-foreground">Code</span>
                <code className="rounded bg-muted px-2 py-1 font-mono text-base tracking-[0.12em]">
                  {session.userCode}
                </code>
                <Button type="button" size="sm" variant="outline" onClick={() => void copyCode()}>
                  {copied ? (
                    <Check className={cn("size-3.5", toneText.success)} />
                  ) : (
                    <Clipboard className="size-3.5" />
                  )}
                  {copied ? "Copied" : "Copy"}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() => void openExternal(verificationUrl)}
                >
                  <ExternalLink className="size-3.5" />
                  Open GitHub
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                Waiting for GitHub confirmation. This usually takes a few seconds after approval.
              </p>
            </div>
          ) : null}

          {phase === "done" ? (
            <p className={cn("flex items-center gap-1.5 text-xs", toneText.success)}>
              <Check className="size-3.5" />
              Copilot credentials saved{login ? ` for ${login}` : ""}.
            </p>
          ) : null}

          {error ? <p className={cn("text-xs", toneText.danger)}>{error}</p> : null}

          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" onClick={() => void start()} disabled={busy}>
              {busy ? (
                <Loader2 className="size-3.5 animate-spin" />
              ) : (
                <GithubIcon className="size-3.5" />
              )}
              {phase === "starting"
                ? "Starting..."
                : phase === "pending"
                  ? "Waiting for GitHub"
                  : phase === "saving"
                    ? "Saving..."
                    : "Connect GitHub Copilot"}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
