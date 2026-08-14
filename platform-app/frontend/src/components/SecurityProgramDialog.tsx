import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Plus, Trash2 } from "lucide-react";

import { FlowField } from "@/components/create-flow/create-flow";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { client } from "@/lib/client";
import {
  CreateSecurityProgramRequestSchema,
  SecurityProgramResourceSchema,
  UpdateSecurityProgramRequestSchema,
  type SecurityProgramResource,
  type SecurityProgramScanTarget,
} from "@/rpc/platform/service_pb";

type ScanTargetDraft = {
  id: number;
  repositoryUrl: string;
  baseBranch: string;
  workflowRef: string;
  policyPackRef: string;
  scanName: string;
  displayName: string;
  priority: string;
  featured: boolean;
};

type ProgramDraft = {
  name: string;
  provider: string;
  displayName: string;
  programUrl: string;
  scopePolicy: string;
  verifiedAt: string;
  scanTargets: ScanTargetDraft[];
};

let nextScanTargetID = 0;

function scanTargetToDraft(source?: SecurityProgramScanTarget, priority = 0): ScanTargetDraft {
  nextScanTargetID += 1;
  return {
    id: nextScanTargetID,
    repositoryUrl: source?.repositoryUrl ?? "",
    baseBranch: source?.baseBranch || "main",
    workflowRef: source?.workflowRef ?? "",
    policyPackRef: source?.policyPackRef ?? "bug-bounty",
    scanName: source?.scanName ?? "",
    displayName: source?.displayName ?? "",
    priority: String(source?.priority ?? priority),
    featured: source?.featured ?? false,
  };
}

function localDateTimeValue(source?: SecurityProgramResource): string {
  if (!source?.verifiedAt) return "";
  const date = timestampDate(source.verifiedAt);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 19);
}

function programToDraft(source?: SecurityProgramResource): ProgramDraft {
  const targets = source?.scanTargets?.length
    ? source.scanTargets
    : source?.scanTarget
      ? [source.scanTarget]
      : [];
  return {
    name: source?.name ?? "",
    provider: source?.provider ?? "",
    displayName: source?.displayName ?? "",
    programUrl: source?.programUrl ?? "",
    scopePolicy: source?.scopePolicy ?? "",
    verifiedAt: localDateTimeValue(source),
    scanTargets: targets.map((target) => scanTargetToDraft(target)),
  };
}

function isHttpsUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "https:" && Boolean(url.host) && !url.username && !url.password;
  } catch {
    return false;
  }
}

function isDNS1123Subdomain(value: string): boolean {
  return value.length <= 253 && /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$/.test(value);
}

function validPriority(value: string): boolean {
  if (!/^\d+$/.test(value)) return false;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 0 && parsed <= 2147483647;
}

export function SecurityProgramDialog({
  source,
  trigger,
  onSaved,
}: {
  source?: SecurityProgramResource;
  trigger: React.ReactElement;
  onSaved: () => void;
}) {
  const isEdit = Boolean(source);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<ProgramDraft>(() => programToDraft(source));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const urlInvalid = draft.programUrl.trim() !== "" && !isHttpsUrl(draft.programUrl.trim());
  const scopePolicyTooLong = Array.from(draft.scopePolicy).length > 131072;
  const scanNameCounts = draft.scanTargets.reduce<Map<string, number>>((counts, target) => {
    const name = target.scanName.trim();
    if (name) counts.set(name, (counts.get(name) ?? 0) + 1);
    return counts;
  }, new Map());
  const scanTargetsInvalid = draft.scanTargets.length > 256 || draft.scanTargets.some((target) => {
    const repositoryUrl = target.repositoryUrl.trim();
    const workflowRef = target.workflowRef.trim();
    const policyPackRef = target.policyPackRef.trim();
    const scanName = target.scanName.trim();
    return !repositoryUrl || repositoryUrl.length > 2048 || !isHttpsUrl(repositoryUrl) ||
      !target.baseBranch.trim() || target.baseBranch.trim().length > 255 ||
      !isDNS1123Subdomain(workflowRef) || !isDNS1123Subdomain(policyPackRef) ||
      !isDNS1123Subdomain(scanName) || (scanNameCounts.get(scanName) ?? 0) > 1 ||
      !target.displayName.trim() || target.displayName.trim().length > 200 ||
      !validPriority(target.priority);
  });
  const blocked =
    !draft.name.trim() ||
    !draft.provider.trim() ||
    !draft.displayName.trim() ||
    !draft.programUrl.trim() ||
    urlInvalid ||
    !draft.scopePolicy.trim() ||
    scopePolicyTooLong ||
    !draft.verifiedAt ||
    scanTargetsInvalid;

  function update<K extends keyof ProgramDraft>(field: K, value: ProgramDraft[K]) {
    setDraft((current) => ({ ...current, [field]: value }));
  }

  function updateScanTarget(index: number, patch: Partial<ScanTargetDraft>) {
    setDraft((current) => ({
      ...current,
      scanTargets: current.scanTargets.map((target, targetIndex) =>
        targetIndex === index ? { ...target, ...patch } : target),
    }));
  }

  function addScanTarget() {
    setDraft((current) => ({
      ...current,
      scanTargets: [
        ...current.scanTargets,
        scanTargetToDraft(undefined, current.scanTargets.length),
      ],
    }));
  }

  function removeScanTarget(index: number) {
    setDraft((current) => ({
      ...current,
      scanTargets: current.scanTargets.filter((_, targetIndex) => targetIndex !== index),
    }));
  }

  function reset() {
    setDraft(programToDraft(source));
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    setSubmitting(true);
    setError(null);
    try {
      const program = create(SecurityProgramResourceSchema, {
        namespace: source?.namespace ?? "",
        name: draft.name.trim(),
        provider: draft.provider.trim(),
        displayName: draft.displayName.trim(),
        programUrl: draft.programUrl.trim(),
        scopePolicy: draft.scopePolicy,
        verifiedAt: timestampFromDate(new Date(draft.verifiedAt)),
        scanTargets: draft.scanTargets.map((target) => ({
          repositoryUrl: target.repositoryUrl.trim(),
          baseBranch: target.baseBranch.trim(),
          workflowRef: target.workflowRef.trim(),
          policyPackRef: target.policyPackRef.trim(),
          scanName: target.scanName.trim(),
          displayName: target.displayName.trim(),
          priority: Number(target.priority),
          featured: target.featured,
        })),
      });
      if (isEdit) {
        await client.updateSecurityProgram(
          create(UpdateSecurityProgramRequestSchema, { program }),
        );
      } else {
        await client.createSecurityProgram(
          create(CreateSecurityProgramRequestSchema, { program }),
        );
      }
      setOpen(false);
      reset();
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save security program");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        // Re-read the latest parent resource every time the editor opens. The
        // row is keyed by name, so it survives a save/refetch and would
        // otherwise reopen with the pre-save draft still in local state.
        reset();
        setOpen(nextOpen);
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-3xl flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl max-h-[92vh]" showCloseButton>
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <DialogTitle className="text-base">
              {isEdit ? `Edit security program ${source?.name}` : "New security program"}
            </DialogTitle>
            <DialogDescription>
              Record an operator-verified scope snapshot. The program URL is provenance only and
              does not authorize network testing.
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 py-5">
            <div className="grid gap-4 sm:grid-cols-2">
              <FlowField id="program-name" label="Name" required>
                <Input
                  id="program-name"
                  value={draft.name}
                  onChange={(event) => update("name", event.target.value)}
                  disabled={isEdit}
                  maxLength={253}
                  placeholder="acme-bugbounty"
                  className="font-mono"
                />
              </FlowField>
              <FlowField id="program-provider" label="Provider" required>
                <Input
                  id="program-provider"
                  value={draft.provider}
                  onChange={(event) => update("provider", event.target.value)}
                  maxLength={100}
                  placeholder="HackerOne"
                />
              </FlowField>
            </div>
            <FlowField id="program-display-name" label="Display name" required>
              <Input
                id="program-display-name"
                value={draft.displayName}
                onChange={(event) => update("displayName", event.target.value)}
                maxLength={200}
                placeholder="Acme public bug bounty"
              />
            </FlowField>
            <FlowField
              id="program-url"
              label="Program URL"
              required
              hint="HTTPS provenance URL only. It is never fetched and grants no authorization to contact a target."
            >
              <Input
                id="program-url"
                type="url"
                value={draft.programUrl}
                onChange={(event) => update("programUrl", event.target.value)}
                maxLength={2048}
                placeholder="https://hackerone.com/acme"
                className="font-mono"
                aria-invalid={urlInvalid}
              />
              {urlInvalid && (
                <p className="pt-1 text-xs text-destructive">Enter an absolute HTTPS URL without user information.</p>
              )}
            </FlowField>
            <section className="space-y-3 border-y border-border/60 py-4" aria-labelledby="scan-targets-heading">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="space-y-1">
                  <h3 id="scan-targets-heading" className="text-sm font-medium">Repository targets</h3>
                  <p className="max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                    One program can cover multiple repositories. Each target becomes an independently
                    importable scan configuration with its own branch and workflow.
                  </p>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={addScanTarget}
                  disabled={draft.scanTargets.length >= 256}
                >
                  <Plus data-icon="inline-start" />
                  Add repository
                </Button>
              </div>
              {draft.scanTargets.length === 0 ? (
                <div className="rounded-lg border border-dashed border-border/70 px-4 py-5 text-center text-xs text-muted-foreground">
                  No repository targets. The verified scope can still be saved without an import shortcut.
                </div>
              ) : (
                <div className="space-y-3">
                  {draft.scanTargets.map((target, index) => {
                    const repositoryUrl = target.repositoryUrl.trim();
                    const scanName = target.scanName.trim();
                    const repositoryUrlInvalid = repositoryUrl !== "" &&
                      (repositoryUrl.length > 2048 || !isHttpsUrl(repositoryUrl));
                    const scanNameInvalid = scanName !== "" && !isDNS1123Subdomain(scanName);
                    const duplicateScanName = scanName !== "" && (scanNameCounts.get(scanName) ?? 0) > 1;
                    const priorityInvalid = target.priority !== "" && !validPriority(target.priority);
                    const fieldID = `program-target-${target.id}`;
                    return (
                      <div key={target.id} className="rounded-xl border border-border/70 bg-muted/20 p-4">
                        <div className="mb-4 flex items-center justify-between gap-3">
                          <div className="min-w-0">
                            <p className="text-xs font-medium">Repository {index + 1}</p>
                            <p className="truncate font-mono text-[10px] text-muted-foreground">
                              {scanName || repositoryUrl || "Unconfigured target"}
                            </p>
                          </div>
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Remove repository ${index + 1}`}
                            onClick={() => removeScanTarget(index)}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                        <div className="grid gap-4 sm:grid-cols-2">
                          <FlowField
                            id={`${fieldID}-repository-url`}
                            label="Repository URL"
                            required
                            className="sm:col-span-2"
                          >
                            <Input
                              id={`${fieldID}-repository-url`}
                              type="url"
                              required
                              maxLength={2048}
                              value={target.repositoryUrl}
                              onChange={(event) => updateScanTarget(index, { repositoryUrl: event.target.value })}
                              placeholder="https://github.com/acme/contracts"
                              className="font-mono"
                              aria-invalid={repositoryUrlInvalid}
                            />
                            {repositoryUrlInvalid && (
                              <p className="pt-1 text-xs text-destructive">Enter an absolute HTTPS URL without user information.</p>
                            )}
                          </FlowField>
                          <FlowField
                            id={`${fieldID}-display-name`}
                            label="Target display name"
                            required
                          >
                            <Input
                              id={`${fieldID}-display-name`}
                              required
                              maxLength={200}
                              value={target.displayName}
                              onChange={(event) => updateScanTarget(index, { displayName: event.target.value })}
                              placeholder="Acme contracts"
                            />
                          </FlowField>
                          <FlowField
                            id={`${fieldID}-scan-name`}
                            label="Scan name"
                            required
                            hint="Unique DNS-style name used for the imported SecurityScan."
                          >
                            <Input
                              id={`${fieldID}-scan-name`}
                              required
                              maxLength={253}
                              value={target.scanName}
                              onChange={(event) => updateScanTarget(index, { scanName: event.target.value })}
                              placeholder="acme-contracts"
                              className="font-mono"
                              aria-invalid={scanNameInvalid || duplicateScanName}
                            />
                            {scanNameInvalid && (
                              <p className="pt-1 text-xs text-destructive">Use a lowercase DNS-style name.</p>
                            )}
                            {duplicateScanName && (
                              <p className="pt-1 text-xs text-destructive">Scan names must be unique within the program.</p>
                            )}
                          </FlowField>
                          <FlowField id={`${fieldID}-base-branch`} label="Default branch" required>
                            <Input
                              id={`${fieldID}-base-branch`}
                              required
                              maxLength={255}
                              value={target.baseBranch}
                              onChange={(event) => updateScanTarget(index, { baseBranch: event.target.value })}
                              placeholder="main"
                              className="font-mono"
                            />
                          </FlowField>
                          <FlowField id={`${fieldID}-workflow-ref`} label="Workflow" required>
                            <Input
                              id={`${fieldID}-workflow-ref`}
                              required
                              maxLength={253}
                              value={target.workflowRef}
                              onChange={(event) => updateScanTarget(index, { workflowRef: event.target.value })}
                              placeholder="smart-contract-review"
                              className="font-mono"
                              aria-invalid={target.workflowRef.trim() !== "" && !isDNS1123Subdomain(target.workflowRef.trim())}
                            />
                          </FlowField>
                          <FlowField id={`${fieldID}-policy-pack-ref`} label="Policy pack" required>
                            <Input
                              id={`${fieldID}-policy-pack-ref`}
                              required
                              maxLength={253}
                              value={target.policyPackRef}
                              onChange={(event) => updateScanTarget(index, { policyPackRef: event.target.value })}
                              placeholder="bug-bounty"
                              className="font-mono"
                              aria-invalid={target.policyPackRef.trim() !== "" && !isDNS1123Subdomain(target.policyPackRef.trim())}
                            />
                          </FlowField>
                          <FlowField id={`${fieldID}-priority`} label="Priority" required hint="Lower values appear first in the import catalog.">
                            <Input
                              id={`${fieldID}-priority`}
                              type="number"
                              required
                              min={0}
                              max={2147483647}
                              step={1}
                              value={target.priority}
                              onChange={(event) => updateScanTarget(index, { priority: event.target.value })}
                              aria-invalid={priorityInvalid}
                            />
                            {priorityInvalid && (
                              <p className="pt-1 text-xs text-destructive">Enter a whole number from 0 to 2,147,483,647.</p>
                            )}
                          </FlowField>
                          <div className="flex items-center justify-between gap-3 self-end rounded-lg border border-border/60 bg-background px-3 py-2">
                            <div className="space-y-0.5">
                              <Label htmlFor={`${fieldID}-featured`} className="text-[12.5px]">Featured target</Label>
                              <p className="text-[10px] text-muted-foreground">Highlight this repository in target pickers.</p>
                            </div>
                            <Switch
                              id={`${fieldID}-featured`}
                              checked={target.featured}
                              onCheckedChange={(featured) => updateScanTarget(index, { featured })}
                            />
                          </div>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </section>
            <FlowField
              id="program-scope-policy"
              label="Scope policy snapshot"
              required
              hint="Paste the explicit in-scope and out-of-scope policy exactly as operator-verified. Scans receive this snapshot as quoted, untrusted data."
            >
              <Textarea
                id="program-scope-policy"
                value={draft.scopePolicy}
                onChange={(event) => update("scopePolicy", event.target.value)}
                aria-invalid={scopePolicyTooLong}
                className="min-h-40 font-mono"
                placeholder={"In scope:\n- api.example.com\n\nOut of scope:\n- production denial-of-service testing"}
              />
              {scopePolicyTooLong && (
                <p className="text-sm text-destructive">Scope policy must be at most 131,072 characters.</p>
              )}
            </FlowField>
            <FlowField
              id="program-verified-at"
              label="Verified at"
              required
              hint="When an operator last checked this snapshot against the authoritative source."
            >
              <Input
                id="program-verified-at"
                type="datetime-local"
                step={1}
                value={draft.verifiedAt}
                onChange={(event) => update("verifiedAt", event.target.value)}
              />
            </FlowField>
            {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          </div>
          <div className="flex justify-end gap-2 border-t px-6 py-4">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || blocked}>
              {submitting ? "Saving…" : isEdit ? "Save security program" : "Create security program"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
