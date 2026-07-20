import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Clipboard, Loader2, Sparkles } from "lucide-react";

import { client } from "@/lib/client";
import {
  completeAnthropicOAuth,
  startAnthropicOAuth,
} from "@/lib/anthropic-oauth";
import { copyText, openExternal } from "@/lib/native";
import { toneSoft, toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

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

interface AnthropicOAuthConnectProps {
  onSaved: (credentials: SavedCredentials) => void;
  className?: string;
}

type Phase = "idle" | "starting" | "awaiting-code" | "exchanging" | "done";

// AnthropicOAuthConnect signs in to a Claude (Pro/Max) account with OAuth.
// Anthropic's flow has no localhost redirect: the user opens the displayed
// sign-in link, approves in the browser, and pastes back the callback code.
// Desktop exchanges it in Rust; web exchanges and stores it on the platform
// server so refresh tokens never pass through browser JavaScript.
export function AnthropicOAuthConnect({ onSaved, className }: AnthropicOAuthConnectProps) {
  const [phase, setPhase] = useState<Phase>("idle");
  const [authorizeUrl, setAuthorizeUrl] = useState("");
  const [sessionId, setSessionId] = useState<string | undefined>();
  const [code, setCode] = useState("");
  const [email, setEmail] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const start = useCallback(async () => {
    setPhase("starting");
    setCode("");
    setEmail(null);
    setCopied(false);
    setError(null);
    try {
      const next = await startAnthropicOAuth();
      if (!mountedRef.current) return;
      setAuthorizeUrl(next.authorizeUrl);
      setSessionId(next.sessionId);
      setPhase("awaiting-code");
    } catch (err) {
      if (!mountedRef.current) return;
      setPhase("idle");
      setError(err instanceof Error ? err.message : "Failed to start Claude sign-in");
    }
  }, []);

  const copyUrl = useCallback(async () => {
    if (!authorizeUrl) return;
    const ok = await copyText(authorizeUrl);
    setCopied(ok);
    if (ok) window.setTimeout(() => setCopied(false), 1800);
  }, [authorizeUrl]);

  const complete = useCallback(async () => {
    const pasted = code.trim();
    if (!pasted) {
      setError("Paste the code shown after approving access");
      return;
    }
    setPhase("exchanging");
    setError(null);
    try {
      const result = await completeAnthropicOAuth(pasted, sessionId);
      let updated = result.credentials;
      if (!updated) {
        if (!result.anthropicOauthJson) throw new Error("Claude sign-in completed without credentials");
        updated = await client.updateMyCredentials({
          anthropicOauthJson: result.anthropicOauthJson,
        });
      }
      if (!mountedRef.current) return;
      setEmail(result.email ?? null);
      onSaved(updated);
      setPhase("done");
      setCode("");
    } catch (err) {
      if (!mountedRef.current) return;
      setPhase("awaiting-code");
      setError(err instanceof Error ? err.message : "Failed to complete Claude sign-in");
    }
  }, [code, onSaved, sessionId]);

  const busy = phase === "starting" || phase === "exchanging";

  return (
    <div className={cn("rounded-lg border bg-muted/20 p-3", className)}>
      <div className="flex items-start gap-3">
        <span className={cn("mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg", toneSoft.neutral)}>
          <Sparkles className="size-4" />
        </span>
        <div className="min-w-0 flex-1 space-y-3">
          <div className="space-y-1">
            <h3 className="text-sm font-medium">Claude</h3>
            <p className="text-xs text-muted-foreground">
              Sign in with your Claude Pro/Max account, then gratefulagents stores refreshable Claude
              credentials for new projects.
            </p>
          </div>

          {phase === "awaiting-code" || phase === "exchanging" ? (
            <div className="space-y-2 rounded-md border bg-background/70 p-3">
              <p className="text-xs text-muted-foreground">
                Click the link below to sign in to Claude, approve access, then paste the code
                shown on the confirmation page.
              </p>
              <div className="flex items-start gap-2">
                <a
                  href={authorizeUrl}
                  onClick={(e) => {
                    e.preventDefault();
                    void openExternal(authorizeUrl);
                  }}
                  className="min-w-0 break-all font-mono text-xs text-primary underline underline-offset-2 hover:opacity-80"
                >
                  {authorizeUrl}
                </a>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="shrink-0"
                  onClick={() => void copyUrl()}
                  disabled={!authorizeUrl}
                >
                  {copied ? (
                    <Check className={cn("size-3.5", toneText.success)} />
                  ) : (
                    <Clipboard className="size-3.5" />
                  )}
                  {copied ? "Copied" : "Copy"}
                </Button>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void complete();
                  }}
                  placeholder="Paste code (code#state)"
                  className="h-8 max-w-[280px] font-mono text-xs"
                  autoComplete="off"
                  disabled={phase === "exchanging"}
                />
                <Button
                  type="button"
                  size="sm"
                  onClick={() => void complete()}
                  disabled={phase === "exchanging" || !code.trim()}
                >
                  {phase === "exchanging" ? <Loader2 className="size-3.5 animate-spin" /> : null}
                  {phase === "exchanging" ? "Connecting..." : "Complete sign-in"}
                </Button>
              </div>
            </div>
          ) : null}

          {phase === "done" ? (
            <p className={cn("flex items-center gap-1.5 text-xs", toneText.success)}>
              <Check className="size-3.5" />
              Claude credentials saved{email ? ` for ${email}` : ""}.
            </p>
          ) : null}

          {error ? <p className={cn("text-xs", toneText.danger)}>{error}</p> : null}

          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" onClick={() => void start()} disabled={busy}>
              {busy ? <Loader2 className="size-3.5 animate-spin" /> : <Sparkles className="size-3.5" />}
              {phase === "starting"
                ? "Starting..."
                : phase === "awaiting-code" || phase === "exchanging"
                  ? "Restart sign-in"
                  : "Connect Claude"}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
