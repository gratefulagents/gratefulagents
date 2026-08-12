import { useState } from "react";
import { create } from "@bufbuild/protobuf";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { client } from "@/lib/client";
import { IMMUNEFI_TARGET_CATALOG } from "@/lib/immunefiTargetCatalog";
import {
  CreateSecurityScanRequestSchema,
  SecurityScanConfigSpecSchema,
} from "@/rpc/platform/service_pb";

export type ImmunefiImportResult = {
  created: number;
  skipped: number;
  failed: { name: string; message: string }[];
};

export function ImmunefiTargetImportDialog({
  existingNames,
  trigger,
  onImported,
}: {
  existingNames: ReadonlySet<string>;
  trigger: React.ReactElement;
  onImported?: (result: ImmunefiImportResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<ImmunefiImportResult | null>(null);
  const skipped = IMMUNEFI_TARGET_CATALOG.filter((target) => existingNames.has(target.name));
  const missing = IMMUNEFI_TARGET_CATALOG.filter((target) => !existingNames.has(target.name));

  async function handleImport() {
    setSubmitting(true);
    setResult(null);
    const failed: ImmunefiImportResult["failed"] = [];
    let created = 0;

    for (const target of missing) {
      try {
        const spec = create(SecurityScanConfigSpecSchema, {
          repoUrl: target.repoUrl,
          workflowRef: target.workflowRef,
          policyPackRef: target.policyPackRef,
          securityProgramRef: target.securityProgramRef,
          schedule: "",
          suspend: false,
          manualOnly: true,
          minSeverity: "high",
          parallelism: 4,
          dedupe: { enabled: true },
          triggers: undefined,
        });
        await client.createSecurityScan(
          create(CreateSecurityScanRequestSchema, {
            namespace: "",
            name: target.name,
            spec,
            useSavedCredentials: true,
          }),
        );
        created += 1;
      } catch (error) {
        failed.push({
          name: target.name,
          message: error instanceof Error ? error.message : "Failed to create scan",
        });
      }
    }

    const nextResult = { created, skipped: skipped.length, failed };
    setResult(nextResult);
    setSubmitting(false);
    onImported?.(nextResult);
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!submitting) {
          setOpen(nextOpen);
          if (nextOpen) setResult(null);
        }
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex max-h-[92vh] w-full max-w-3xl flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <DialogHeader className="space-y-1 border-b px-6 py-5">
          <DialogTitle>Import Immunefi targets</DialogTitle>
          <DialogDescription>
            Preview the 20 approved repository targets. Existing configurations with the same
            name are skipped and never modified.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
          <p className="mb-4 rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm font-medium">
            Nothing runs automatically. These manual-only configurations run only when you choose Run now.
          </p>
          <ul className="divide-y rounded-lg border" aria-label="Approved Immunefi targets">
            {IMMUNEFI_TARGET_CATALOG.map((target) => {
              const exists = existingNames.has(target.name);
              return (
                <li key={target.name} className="grid gap-1 px-3 py-2.5 sm:grid-cols-[1fr_auto]">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{target.displayName}</span>
                      {exists && (
                        <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
                          Existing name — skipped
                        </span>
                      )}
                    </div>
                    <div className="truncate font-mono text-xs text-muted-foreground">{target.repoUrl}</div>
                  </div>
                  <div className="font-mono text-xs text-muted-foreground sm:text-right">
                    {target.workflowRef}
                  </div>
                </li>
              );
            })}
          </ul>
          {result && (
            <div className="mt-4 space-y-2" role="status" aria-live="polite">
              <p className="text-sm font-medium">
                Created {result.created}; skipped {result.skipped}; failed {result.failed.length}.
              </p>
              {result.failed.length > 0 && (
                <ul className="space-y-1 text-sm text-destructive">
                  {result.failed.map((failure) => (
                    <li key={failure.name}>
                      <span className="font-mono">{failure.name}</span>: {failure.message}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>
        <div className="flex justify-end gap-2 border-t px-6 py-4">
          <Button type="button" variant="ghost" disabled={submitting} onClick={() => setOpen(false)}>
            Close
          </Button>
          <Button type="button" disabled={submitting || missing.length === 0} onClick={() => void handleImport()}>
            {submitting ? "Importing…" : `Import ${missing.length} missing target${missing.length === 1 ? "" : "s"}`}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
