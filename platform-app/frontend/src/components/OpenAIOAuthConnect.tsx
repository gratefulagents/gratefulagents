import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Bot, Check, Clipboard, ExternalLink, Loader2 } from "lucide-react";

import { client } from "@/lib/client";
import {
  cancelOpenAIOAuth,
  pollOpenAIOAuth,
  startOpenAIDeviceOAuth,
  startOpenAIOAuth,
  type OpenAIOAuthStart,
} from "@/lib/openai-oauth";
import { copyText, openExternal } from "@/lib/native";
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

interface OpenAIOAuthConnectProps {
  /** Render only the flow body — no card chrome or header (for embedding in a provider panel). */
  compact?: boolean;
  onSaved: (credentials: SavedCredentials) => void;
  className?: string;
}

type Phase = "idle" | "starting" | "pending" | "saving" | "done";

// Desktop uses a browser PKCE flow with a local callback and device fallback.
// Web uses the no-port device flow; the platform server performs the token
// exchange and stores refreshable credentials in the user's namespace.
export function OpenAIOAuthConnect({ onSaved, className, compact }: OpenAIOAuthConnectProps) {
  const [phase, setPhase] = useState<Phase>("idle");
  const [session, setSession] = useState<OpenAIOAuthStart | null>(null);
  const [pollDelay, setPollDelay] = useState(2);
  const [email, setEmail] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [offerDeviceFlow, setOfferDeviceFlow] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      void cancelOpenAIOAuth();
    };
  }, []);

  const openTarget = useMemo(() => {
    if (!session) return "";
    return session.mode === "device"
      ? session.verificationUri || ""
      : session.authorizeUrl || "";
  }, [session]);

  const start = useCallback(async (mode: "browser" | "device") => {
    setPhase("starting");
    setSession(null);
    setEmail(null);
    setCopied(false);
    setError(null);
    try {
      const next = mode === "device" ? await startOpenAIDeviceOAuth() : await startOpenAIOAuth();
      if (!mountedRef.current) return;
      setSession(next);
      setPollDelay(next.mode === "device" ? Math.max(next.interval || 5, 5) : 2);
      setPhase("pending");
      const url = next.mode === "device" ? next.verificationUri : next.authorizeUrl;
      if (url) void openExternal(url);
    } catch (err) {
      if (!mountedRef.current) return;
      setPhase("idle");
      if (mode === "browser") setOfferDeviceFlow(true);
      setError(err instanceof Error ? err.message : "Failed to start ChatGPT sign-in");
    }
  }, []);

  useEffect(() => {
    if (phase !== "pending" || !session) return;

    const activeSession = session;
    let cancelled = false;
    let timeout: number | undefined;

    async function tick() {
      try {
        const result = await pollOpenAIOAuth(activeSession.sessionId);
        if (cancelled) return;

        if (result.status === "completed") {
          setPhase("saving");
          setEmail(result.email ?? null);
          let updated = result.credentials;
          if (!updated) {
            if (!result.openaiOauthJson) throw new Error("ChatGPT sign-in completed without credentials");
            updated = await client.updateMyCredentials({
              openaiOauthJson: result.openaiOauthJson,
              openaiAccountId: result.accountId ?? "",
            });
          }
          if (!mountedRef.current) return;
          onSaved(updated);
          setPhase("done");
          return;
        }

        if (result.status === "pending") {
          timeout = window.setTimeout(tick, pollDelay * 1000);
          return;
        }

        setPhase("idle");
        setError(result.error || "ChatGPT sign-in did not complete");
      } catch (err) {
        if (cancelled) return;
        setPhase("idle");
        setError(err instanceof Error ? err.message : "Failed to complete ChatGPT sign-in");
      }
    }

    timeout = window.setTimeout(tick, pollDelay * 1000);
    return () => {
      cancelled = true;
      if (timeout !== undefined) window.clearTimeout(timeout);
    };
  }, [onSaved, phase, pollDelay, session]);

  const copyCode = useCallback(async () => {
    if (!session?.userCode) return;
    const ok = await copyText(session.userCode);
    setCopied(ok);
    if (ok) window.setTimeout(() => setCopied(false), 1800);
  }, [session]);

  const busy = phase === "starting" || phase === "pending" || phase === "saving";

  return (
    <div className={cn(!compact && "rounded-lg border bg-muted/20 p-3", className)}>
      <div className="flex items-start gap-3">
        {!compact && (
          <span className={cn("mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg", toneSoft.neutral)}>
            <Bot className="size-4" />
          </span>
        )}
        <div className="min-w-0 flex-1 space-y-3">
          {!compact && (
            <div className="space-y-1">
              <h3 className="text-sm font-medium">OpenAI OAuth</h3>
              <p className="text-xs text-muted-foreground">
                Sign in with OpenAI, then gratefulagents stores refreshable credentials for new
                projects.
              </p>
            </div>
          )}

          {session && phase !== "done" ? (
            <div className="space-y-2 rounded-md border bg-background/70 p-3">
              {session.mode === "device" && session.userCode ? (
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
                    onClick={() => void openExternal(openTarget)}
                  >
                    <ExternalLink className="size-3.5" />
                    Open ChatGPT
                  </Button>
                </div>
              ) : (
                <div className="flex flex-wrap items-center gap-2">
                  <p className="text-xs text-muted-foreground">
                    Finish signing in with ChatGPT in your browser.
                  </p>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => void openExternal(openTarget)}
                  >
                    <ExternalLink className="size-3.5" />
                    Reopen browser
                  </Button>
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                Waiting for approval. This usually takes a few seconds.
              </p>
            </div>
          ) : null}

          {phase === "done" ? (
            <p className={cn("flex items-center gap-1.5 text-xs", toneText.success)}>
              <Check className="size-3.5" />
              ChatGPT credentials saved{email ? ` for ${email}` : ""}.
            </p>
          ) : null}

          {error ? <p className={cn("text-xs", toneText.danger)}>{error}</p> : null}

          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" onClick={() => void start("browser")} disabled={busy}>
              {busy ? <Loader2 className="size-3.5 animate-spin" /> : <Bot className="size-3.5" />}
              {phase === "starting"
                ? "Starting..."
                : phase === "pending"
                  ? "Waiting for ChatGPT"
                  : phase === "saving"
                    ? "Saving..."
                    : "Sign in with ChatGPT"}
            </Button>
            {offerDeviceFlow ? (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => void start("device")}
                disabled={busy}
              >
                Use device code instead
              </Button>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  );
}
