import { useCallback, useEffect, useState } from "react";
import { Check, Download, Loader2, ShieldCheck, X } from "lucide-react";
import { client } from "@/lib/client";
import { isTauri } from "@/lib/platform";
import { cn } from "@/lib/utils";
import { toneSoft, toneText } from "@/lib/status";
import { Button } from "@/components/ui/button";
import {
  credentialsToUpdate,
  detectLocalCredentials,
  type LocalCredential,
} from "@/lib/local-creds";

interface ImportLocalCredentialsProps {
  // Called after a successful import so the parent can refresh chat settings.
  onImported?: () => void;
  className?: string;
}

// ImportLocalCredentials offers a one-click import of provider credentials
// found on the user's machine. It renders nothing on the
// web build, while detecting, or when no local credentials are present — so it is
// safe to drop into onboarding and settings unconditionally.
export function ImportLocalCredentials({ onImported, className }: ImportLocalCredentialsProps) {
  const [creds, setCreds] = useState<LocalCredential[]>([]);
  const [loading, setLoading] = useState(isTauri);
  const [importing, setImporting] = useState(false);
  const [imported, setImported] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!isTauri) return;
    let active = true;
    void detectLocalCredentials().then((found) => {
      if (!active) return;
      setCreds(found);
      setLoading(false);
    });
    return () => {
      active = false;
    };
  }, []);

  const importAll = useCallback(async () => {
    if (creds.length === 0) return;
    setImporting(true);
    setError(null);
    try {
      await client.updateMyCredentials(credentialsToUpdate(creds));
      setImported(true);
      onImported?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to import credentials");
    } finally {
      setImporting(false);
    }
  }, [creds, onImported]);

  if (!isTauri || dismissed || loading || creds.length === 0) return null;

  return (
    <div
      className={cn(
        "surface-card relative rounded-xl border p-4",
        className,
      )}
    >
      {!imported && (
        <button
          type="button"
          onClick={() => setDismissed(true)}
          aria-label="Dismiss"
          className="absolute right-3 top-3 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        >
          <X className="size-4" />
        </button>
      )}

      <div className="flex items-start gap-3">
        <span className={cn("mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg", toneSoft.success)}>
          <ShieldCheck className="size-4" />
        </span>
        <div className="min-w-0 flex-1 space-y-3">
          <div className="space-y-1">
            <h3 className="text-sm font-medium">
              {imported ? "Credentials imported" : "Use your local CLI sign-in"}
            </h3>
            <p className="text-xs text-muted-foreground">
              {imported
                ? "Your provider credentials are ready — pick a model and start working."
                : "We found provider credentials from your installed CLIs. Import them to start without pasting keys."}
            </p>
          </div>

          <ul className="flex flex-wrap gap-1.5">
            {creds.map((cred) => (
              <li
                key={cred.provider + cred.sourcePath}
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs",
                  toneSoft.neutral,
                )}
              >
                {imported ? (
                  <Check className={cn("size-3", toneText.success)} />
                ) : null}
                <span className="font-medium">{cred.label}</span>
                {cred.account ? (
                  <span className="text-muted-foreground">· {cred.account}</span>
                ) : null}
              </li>
            ))}
          </ul>

          {error ? <p className={cn("text-xs", toneText.danger)}>{error}</p> : null}

          {!imported && (
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={() => void importAll()} disabled={importing}>
                {importing ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <Download className="size-3.5" />
                )}
                Import {creds.length > 1 ? `all (${creds.length})` : creds[0]?.label}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setDismissed(true)} disabled={importing}>
                Not now
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
