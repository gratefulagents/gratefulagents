/* eslint-disable react-refresh/only-export-components */
import { useCallback, useRef, useState } from "react";

import { client } from "@/lib/client";
import { providerLabel } from "@/lib/model-providers";
import { toneText } from "@/lib/status";
import { cn } from "@/lib/utils";

export type ApiKeyVerification =
  | { status: "verifying" }
  | { status: "verified"; models: string[] }
  | { status: "failed"; message: string };

/**
 * useApiKeyVerification probes just-saved provider API keys with a live
 * models fetch: listAvailableModels with no credential refs routes to the
 * caller's saved credentials, so calling it right after updateMyCredentials
 * resolves is a real key check with zero extra plumbing. Advisory only — the
 * key stays saved whatever the probe says. Only api_key auth mode is probed;
 * OAuth flows prove themselves.
 */
export function useApiKeyVerification() {
  const [verifications, setVerifications] = useState<Record<string, ApiKeyVerification>>({});
  // Attempt counters so a re-save supersedes any in-flight probe's result.
  const attempts = useRef<Record<string, number>>({});

  const verify = useCallback((namespace: string, providers: string[]) => {
    for (const provider of providers) {
      const attempt = (attempts.current[provider] ?? 0) + 1;
      attempts.current[provider] = attempt;
      setVerifications((v) => ({ ...v, [provider]: { status: "verifying" } }));
      void client.listAvailableModels({ namespace, provider, authMode: "api-key" }).then(
        (response) => {
          if (attempts.current[provider] !== attempt) return;
          setVerifications((v) => ({
            ...v,
            [provider]: { status: "verified", models: response.models },
          }));
        },
        (cause: unknown) => {
          if (attempts.current[provider] !== attempt) return;
          setVerifications((v) => ({
            ...v,
            [provider]: {
              status: "failed",
              message: cause instanceof Error ? cause.message : "provider did not respond",
            },
          }));
        },
      );
    }
  }, []);

  return { verifications, verify };
}

/** One-line advisory verdict for a just-saved key; renders nothing until a probe ran. */
export function ApiKeyVerificationNote({
  provider,
  state,
}: {
  provider: string;
  state?: ApiKeyVerification;
}) {
  if (!state) return null;
  const label = providerLabel(provider);
  if (state.status === "verifying") {
    return <p className="text-[11.5px] text-muted-foreground">Verifying {label} key…</p>;
  }
  if (state.status === "verified") {
    const count = state.models.length;
    const example = state.models[0];
    return (
      <p className={cn("text-[11.5px]", toneText.success)}>
        ✓ {label} key verified — {count} {count === 1 ? "model" : "models"} available
        {example ? ` (e.g. ${example})` : ""}
      </p>
    );
  }
  return (
    <p role="status" className={cn("text-[11.5px]", toneText.warning)}>
      {label} key saved, but verification failed: {state.message}. Check the key and try again.
    </p>
  );
}
