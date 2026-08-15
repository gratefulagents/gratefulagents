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
  targetType: "repository" | "website";
  repositoryUrl: string;
  targetUrl: string;
  baseBranch: string;
  workflowRef: string;
  policyPackRef: string;
  scanName: string;
  displayName: string;
  priority: string;
  featured: boolean;
};

type ImpactDraft = {
  id: number;
  impact: string;
  level: string;
  assetType: string;
};

type AssetDraft = {
  id: number;
  chainId: string;
  address: string;
  repositoryUrl: string;
  displayName: string;
  addedOn: string;
};

type KnownIssueDraft = {
  id: number;
  source: string;
  summary: string;
  reference: string;
};

type ProgramDraft = {
  name: string;
  provider: string;
  displayName: string;
  programUrl: string;
  scopePolicy: string;
  verifiedAt: string;
  scanTargets: ScanTargetDraft[];
  severitySystem: string;
  primacy: string;
  pocRequired: boolean;
  pocEnvironment: string;
  kycRequired: boolean;
  inScopeImpacts: ImpactDraft[];
  outOfScope: string;
  prohibitedTesting: string;
  assets: AssetDraft[];
  knownIssues: KnownIssueDraft[];
  budgetMaxPerPeriod: string;
  budgetPeriodDays: string;
  budgetUnrestrictedRequiresReputation: boolean;
};

const selectClass = "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

const SEVERITY_SYSTEM_OPTIONS = [
  "immunefi-v2.3",
  "code4rena",
  "sherlock",
  "cantina",
  "ethereum-foundation",
  "custom",
] as const;

const IMPACT_LEVEL_OPTIONS = ["critical", "high", "medium", "low"] as const;

const PRIMACY_OPTIONS = [
  { value: "impact", label: "Impact — demonstrated impact decides eligibility" },
  { value: "rules", label: "Rules — only itemized assets are eligible" },
] as const;

const POC_ENVIRONMENT_OPTIONS = [
  { value: "mainnet-fork", label: "Mainnet fork" },
  { value: "project-test-suite", label: "Project test suite" },
  { value: "local-devnet", label: "Local devnet" },
  { value: "either", label: "Either" },
] as const;

let nextScanTargetID = 0;
let nextScopeRowID = 0;

function nextRowID(): number {
  nextScopeRowID += 1;
  return nextScopeRowID;
}

function linesToDraft(values: readonly string[] | undefined): string {
  return (values ?? []).join("\n");
}

function draftToLines(value: string): string[] {
  return value.split("\n").map((line) => line.trim()).filter(Boolean);
}

function scanTargetToDraft(source?: SecurityProgramScanTarget, priority = 0): ScanTargetDraft {
  nextScanTargetID += 1;
  return {
    id: nextScanTargetID,
    targetType: source?.targetUrl ? "website" : "repository",
    repositoryUrl: source?.repositoryUrl ?? "",
    targetUrl: source?.targetUrl ?? "",
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
    severitySystem: source?.severitySystem ?? "",
    primacy: source?.primacy ?? "",
    pocRequired: source?.pocRequired ?? false,
    pocEnvironment: source?.pocEnvironment ?? "",
    kycRequired: source?.kycRequired ?? false,
    inScopeImpacts: (source?.inScopeImpacts ?? []).map((impact) => ({
      id: nextRowID(),
      impact: impact.impact,
      level: impact.level,
      assetType: impact.assetType,
    })),
    outOfScope: linesToDraft(source?.outOfScope),
    prohibitedTesting: linesToDraft(source?.prohibitedTesting),
    assets: (source?.assets ?? []).map((asset) => ({
      id: nextRowID(),
      chainId: asset.chainId,
      address: asset.address,
      repositoryUrl: asset.repositoryUrl,
      displayName: asset.displayName,
      addedOn: asset.addedOn,
    })),
    knownIssues: (source?.knownIssues ?? []).map((issue) => ({
      id: nextRowID(),
      source: issue.source,
      summary: issue.summary,
      reference: issue.reference,
    })),
    budgetMaxPerPeriod: source?.submissionBudget?.maxPerPeriod
      ? String(source.submissionBudget.maxPerPeriod)
      : "",
    budgetPeriodDays: source?.submissionBudget?.periodDays
      ? String(source.submissionBudget.periodDays)
      : "",
    budgetUnrestrictedRequiresReputation:
      source?.submissionBudget?.unrestrictedRequiresReputation ?? false,
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

function isHttpUrlOrDomain(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed || trimmed.includes(",")) return false;
  const absolute = trimmed.includes("://");
  if (!absolute && /[/?#@]/.test(trimmed)) return false;
  const candidate = absolute ? trimmed : `https://${trimmed}`;
  try {
    const url = new URL(candidate);
    return (url.protocol === "https:" || url.protocol === "http:") &&
      Boolean(url.hostname) && !url.username && !url.password && !url.hash;
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

function validCount(value: string, max: number): boolean {
  if (!/^\d+$/.test(value)) return false;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 0 && parsed <= max;
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
    const targetUrl = target.targetUrl.trim();
    const workflowRef = target.workflowRef.trim();
    const policyPackRef = target.policyPackRef.trim();
    const scanName = target.scanName.trim();
    const targetInvalid = target.targetType === "repository"
      ? !repositoryUrl || repositoryUrl.length > 2048 || !isHttpsUrl(repositoryUrl) ||
        !target.baseBranch.trim() || target.baseBranch.trim().length > 255
      : !targetUrl || targetUrl.length > 2048 || !isHttpUrlOrDomain(targetUrl);
    return targetInvalid ||
      !isDNS1123Subdomain(workflowRef) || !isDNS1123Subdomain(policyPackRef) ||
      !isDNS1123Subdomain(scanName) || (scanNameCounts.get(scanName) ?? 0) > 1 ||
      !target.displayName.trim() || target.displayName.trim().length > 200 ||
      !validPriority(target.priority);
  });
  const impactErrors = draft.inScopeImpacts.map((impact) => {
    if (!impact.impact.trim()) return "Copy the impact clause verbatim from the program page.";
    if (Array.from(impact.impact).length > 1024) return "Impact clause must be at most 1,024 characters.";
    if (!IMPACT_LEVEL_OPTIONS.includes(impact.level as (typeof IMPACT_LEVEL_OPTIONS)[number])) {
      return "Choose the severity the program itself assigns this impact.";
    }
    if (draft.severitySystem === "sherlock" && (impact.level === "low" || impact.level === "critical")) {
      return "Sherlock judges only high and medium severities.";
    }
    return "";
  });
  const assetErrors = draft.assets.map((asset) => {
    const chainId = asset.chainId.trim();
    const address = asset.address.trim();
    const repositoryUrl = asset.repositoryUrl.trim();
    if (!chainId && !address && !repositoryUrl) {
      return "Identify the asset by chain ID and address, or by repository URL.";
    }
    if (address && !chainId) return "Chain ID is required when an address is set.";
    if (repositoryUrl && !isHttpsUrl(repositoryUrl)) {
      return "Enter an absolute HTTPS URL without user information.";
    }
    return "";
  });
  const knownIssueErrors = draft.knownIssues.map((issue) => {
    if (!issue.source.trim()) return "Record where the known issue comes from.";
    if (!issue.summary.trim()) return "Summarize the known issue in the program's own words.";
    if (issue.reference.trim() && !isHttpsUrl(issue.reference.trim())) {
      return "Enter an absolute HTTPS URL without user information.";
    }
    return "";
  });
  const budgetCapInvalid = draft.budgetMaxPerPeriod !== "" && !validCount(draft.budgetMaxPerPeriod, 10000);
  const budgetPeriodInvalid = draft.budgetPeriodDays !== "" && !validCount(draft.budgetPeriodDays, 365);
  const budgetCapMissing =
    !budgetPeriodInvalid && Number(draft.budgetPeriodDays || 0) > 0 && Number(draft.budgetMaxPerPeriod || 0) === 0;
  const budgetSet =
    Number(draft.budgetMaxPerPeriod || 0) > 0 || Number(draft.budgetPeriodDays || 0) > 0 ||
    draft.budgetUnrestrictedRequiresReputation;
  const typedScopeInvalid =
    impactErrors.some(Boolean) || assetErrors.some(Boolean) || knownIssueErrors.some(Boolean) ||
    budgetCapInvalid || budgetPeriodInvalid || budgetCapMissing;
  const blocked =
    !draft.name.trim() ||
    !draft.provider.trim() ||
    !draft.displayName.trim() ||
    !draft.programUrl.trim() ||
    urlInvalid ||
    !draft.scopePolicy.trim() ||
    scopePolicyTooLong ||
    !draft.verifiedAt ||
    scanTargetsInvalid ||
    typedScopeInvalid;

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

  function updateImpact(index: number, patch: Partial<ImpactDraft>) {
    setDraft((current) => ({
      ...current,
      inScopeImpacts: current.inScopeImpacts.map((impact, impactIndex) =>
        impactIndex === index ? { ...impact, ...patch } : impact),
    }));
  }

  function addImpact() {
    setDraft((current) => ({
      ...current,
      inScopeImpacts: [...current.inScopeImpacts, { id: nextRowID(), impact: "", level: "", assetType: "" }],
    }));
  }

  function removeImpact(index: number) {
    setDraft((current) => ({
      ...current,
      inScopeImpacts: current.inScopeImpacts.filter((_, impactIndex) => impactIndex !== index),
    }));
  }

  function updateAsset(index: number, patch: Partial<AssetDraft>) {
    setDraft((current) => ({
      ...current,
      assets: current.assets.map((asset, assetIndex) =>
        assetIndex === index ? { ...asset, ...patch } : asset),
    }));
  }

  function addAsset() {
    setDraft((current) => ({
      ...current,
      assets: [
        ...current.assets,
        { id: nextRowID(), chainId: "", address: "", repositoryUrl: "", displayName: "", addedOn: "" },
      ],
    }));
  }

  function removeAsset(index: number) {
    setDraft((current) => ({
      ...current,
      assets: current.assets.filter((_, assetIndex) => assetIndex !== index),
    }));
  }

  function updateKnownIssue(index: number, patch: Partial<KnownIssueDraft>) {
    setDraft((current) => ({
      ...current,
      knownIssues: current.knownIssues.map((issue, issueIndex) =>
        issueIndex === index ? { ...issue, ...patch } : issue),
    }));
  }

  function addKnownIssue() {
    setDraft((current) => ({
      ...current,
      knownIssues: [...current.knownIssues, { id: nextRowID(), source: "", summary: "", reference: "" }],
    }));
  }

  function removeKnownIssue(index: number) {
    setDraft((current) => ({
      ...current,
      knownIssues: current.knownIssues.filter((_, issueIndex) => issueIndex !== index),
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
          repositoryUrl: target.targetType === "repository" ? target.repositoryUrl.trim() : "",
          targetUrl: target.targetType === "website" ? target.targetUrl.trim() : "",
          baseBranch: target.targetType === "repository" ? target.baseBranch.trim() : "",
          workflowRef: target.workflowRef.trim(),
          policyPackRef: target.policyPackRef.trim(),
          scanName: target.scanName.trim(),
          displayName: target.displayName.trim(),
          priority: Number(target.priority),
          featured: target.featured,
        })),
        severitySystem: draft.severitySystem,
        primacy: draft.primacy,
        pocRequired: draft.pocRequired,
        pocEnvironment: draft.pocEnvironment,
        kycRequired: draft.kycRequired,
        inScopeImpacts: draft.inScopeImpacts.map((impact) => ({
          impact: impact.impact.trim(),
          level: impact.level,
          assetType: impact.assetType.trim(),
        })),
        outOfScope: draftToLines(draft.outOfScope),
        prohibitedTesting: draftToLines(draft.prohibitedTesting),
        assets: draft.assets.map((asset) => ({
          chainId: asset.chainId.trim(),
          address: asset.address.trim(),
          repositoryUrl: asset.repositoryUrl.trim(),
          displayName: asset.displayName.trim(),
          addedOn: asset.addedOn.trim(),
        })),
        knownIssues: draft.knownIssues.map((issue) => ({
          source: issue.source.trim(),
          summary: issue.summary.trim(),
          reference: issue.reference.trim(),
        })),
        submissionBudget: budgetSet
          ? {
            maxPerPeriod: Number(draft.budgetMaxPerPeriod || 0),
            periodDays: Number(draft.budgetPeriodDays || 0),
            unrestrictedRequiresReputation: draft.budgetUnrestrictedRequiresReputation,
          }
          : undefined,
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
            <section className="space-y-4 border-y border-border/60 py-4" aria-labelledby="published-scope-heading">
              <div className="space-y-1">
                <h3 id="published-scope-heading" className="text-sm font-medium">Published scope facts</h3>
                <p className="max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                  Transcribe what the program published, verbatim. A report may only claim an impact
                  the program itself lists, so clauses are matched literally and are never reworded,
                  summarized, or translated between severity systems.
                </p>
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField
                  id="program-severity-system"
                  label="Severity system"
                  hint="The ladder this program judges under. Severities are never translated between systems."
                >
                  <select
                    id="program-severity-system"
                    className={selectClass}
                    value={draft.severitySystem}
                    onChange={(event) => update("severitySystem", event.target.value)}
                  >
                    <option value="">not transcribed</option>
                    {SEVERITY_SYSTEM_OPTIONS.map((system) => (
                      <option key={system} value={system}>{system}</option>
                    ))}
                  </select>
                </FlowField>
                <FlowField
                  id="program-primacy"
                  label="Primacy"
                  hint="Whether the program judges a report by demonstrated impact or strictly by its itemized assets."
                >
                  <select
                    id="program-primacy"
                    className={selectClass}
                    value={draft.primacy}
                    onChange={(event) => update("primacy", event.target.value)}
                  >
                    <option value="">not transcribed</option>
                    {PRIMACY_OPTIONS.map((option) => (
                      <option key={option.value} value={option.value}>{option.label}</option>
                    ))}
                  </select>
                </FlowField>
              </div>
              <div className="space-y-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="space-y-1">
                    <h4 className="text-xs font-medium">In-scope impacts</h4>
                    <p className="max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                      Copy each impact clause verbatim from the program page: a report may only claim
                      an impact the program published, and rewording one is a rules violation rather
                      than a downgrade. Use the level the program's own system assigns.
                    </p>
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={addImpact}
                    disabled={draft.inScopeImpacts.length >= 256}
                  >
                    <Plus data-icon="inline-start" />
                    Add impact
                  </Button>
                </div>
                {draft.inScopeImpacts.map((impact, index) => {
                  const fieldID = `program-impact-${impact.id}`;
                  return (
                    <div key={impact.id} className="rounded-xl border border-border/70 bg-muted/20 p-4">
                      <div className="mb-3 flex items-center justify-between gap-3">
                        <p className="text-xs font-medium">Impact {index + 1}</p>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Remove impact ${index + 1}`}
                          onClick={() => removeImpact(index)}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                      <div className="grid gap-4 sm:grid-cols-2">
                        <FlowField id={`${fieldID}-clause`} label="Impact clause" required className="sm:col-span-2">
                          <Textarea
                            id={`${fieldID}-clause`}
                            value={impact.impact}
                            onChange={(event) => updateImpact(index, { impact: event.target.value })}
                            className="min-h-16"
                            placeholder="Permanent freezing of funds"
                            aria-invalid={Boolean(impactErrors[index])}
                          />
                        </FlowField>
                        <FlowField id={`${fieldID}-level`} label="Program severity" required>
                          <select
                            id={`${fieldID}-level`}
                            className={selectClass}
                            value={impact.level}
                            onChange={(event) => updateImpact(index, { level: event.target.value })}
                          >
                            <option value="">choose level</option>
                            {IMPACT_LEVEL_OPTIONS.map((level) => (
                              <option key={level} value={level}>{level}</option>
                            ))}
                          </select>
                        </FlowField>
                        <FlowField
                          id={`${fieldID}-asset-type`}
                          label="Asset category"
                          hint="The program's own category, such as Smart Contract."
                        >
                          <Input
                            id={`${fieldID}-asset-type`}
                            maxLength={200}
                            value={impact.assetType}
                            onChange={(event) => updateImpact(index, { assetType: event.target.value })}
                            placeholder="Smart Contract"
                          />
                        </FlowField>
                      </div>
                      {impactErrors[index] && (
                        <p className="pt-2 text-xs text-destructive">{impactErrors[index]}</p>
                      )}
                    </div>
                  );
                })}
              </div>
              <FlowField
                id="program-out-of-scope"
                label="Out of scope"
                hint="One published exclusion per line, transcribed verbatim."
              >
                <Textarea
                  id="program-out-of-scope"
                  value={draft.outOfScope}
                  onChange={(event) => update("outOfScope", event.target.value)}
                  className="min-h-20"
                  placeholder={"Attacks requiring leaked keys\nBest-practice recommendations"}
                />
              </FlowField>
              <FlowField
                id="program-prohibited-testing"
                label="Prohibited testing"
                hint="One published prohibition per line, transcribed verbatim. Violating one typically forfeits every report."
              >
                <Textarea
                  id="program-prohibited-testing"
                  value={draft.prohibitedTesting}
                  onChange={(event) => update("prohibitedTesting", event.target.value)}
                  className="min-h-20"
                  placeholder={"Testing on mainnet or public testnet\nPhishing program staff"}
                />
              </FlowField>
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="flex items-center justify-between gap-3 rounded-lg border border-border/60 bg-background px-3 py-2">
                  <div className="space-y-0.5">
                    <Label htmlFor="program-poc-required" className="text-[12.5px]">PoC required</Label>
                    <p className="text-[10px] text-muted-foreground">The program only reads reports with a runnable proof of concept.</p>
                  </div>
                  <Switch
                    id="program-poc-required"
                    checked={draft.pocRequired}
                    onCheckedChange={(pocRequired) => update("pocRequired", pocRequired)}
                  />
                </div>
                <FlowField
                  id="program-poc-environment"
                  label="PoC environment"
                  hint="Where the program accepts a proof of concept being run."
                >
                  <select
                    id="program-poc-environment"
                    className={selectClass}
                    value={draft.pocEnvironment}
                    onChange={(event) => update("pocEnvironment", event.target.value)}
                  >
                    <option value="">not transcribed</option>
                    {POC_ENVIRONMENT_OPTIONS.map((option) => (
                      <option key={option.value} value={option.value}>{option.label}</option>
                    ))}
                  </select>
                </FlowField>
              </div>
              <div className="space-y-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="space-y-1">
                    <h4 className="text-xs font-medium">Deployed assets</h4>
                    <p className="max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                      Bind each in-scope asset to what is actually deployed. Identify it by chain ID
                      and address, by repository URL, or by both.
                    </p>
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={addAsset}
                    disabled={draft.assets.length >= 256}
                  >
                    <Plus data-icon="inline-start" />
                    Add asset
                  </Button>
                </div>
                {draft.assets.map((asset, index) => {
                  const fieldID = `program-asset-${asset.id}`;
                  return (
                    <div key={asset.id} className="rounded-xl border border-border/70 bg-muted/20 p-4">
                      <div className="mb-3 flex items-center justify-between gap-3">
                        <p className="text-xs font-medium">Asset {index + 1}</p>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Remove asset ${index + 1}`}
                          onClick={() => removeAsset(index)}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                      <div className="grid gap-4 sm:grid-cols-2">
                        <FlowField id={`${fieldID}-chain-id`} label="Chain ID" hint="EIP-155 or the chain's own network identifier.">
                          <Input
                            id={`${fieldID}-chain-id`}
                            maxLength={64}
                            value={asset.chainId}
                            onChange={(event) => updateAsset(index, { chainId: event.target.value })}
                            placeholder="1"
                            className="font-mono"
                          />
                        </FlowField>
                        <FlowField id={`${fieldID}-address`} label="Address">
                          <Input
                            id={`${fieldID}-address`}
                            maxLength={128}
                            value={asset.address}
                            onChange={(event) => updateAsset(index, { address: event.target.value })}
                            placeholder="0x0000000000000000000000000000000000000000"
                            className="font-mono"
                          />
                        </FlowField>
                        <FlowField id={`${fieldID}-repository-url`} label="Asset repository URL" className="sm:col-span-2">
                          <Input
                            id={`${fieldID}-repository-url`}
                            type="url"
                            maxLength={2048}
                            value={asset.repositoryUrl}
                            onChange={(event) => updateAsset(index, { repositoryUrl: event.target.value })}
                            placeholder="https://github.com/acme/contracts"
                            className="font-mono"
                          />
                        </FlowField>
                        <FlowField id={`${fieldID}-display-name`} label="Asset name">
                          <Input
                            id={`${fieldID}-display-name`}
                            maxLength={200}
                            value={asset.displayName}
                            onChange={(event) => updateAsset(index, { displayName: event.target.value })}
                            placeholder="Acme vault"
                          />
                        </FlowField>
                        <FlowField id={`${fieldID}-added-on`} label="Added on" hint="The date the program listed the asset, as published.">
                          <Input
                            id={`${fieldID}-added-on`}
                            maxLength={64}
                            value={asset.addedOn}
                            onChange={(event) => updateAsset(index, { addedOn: event.target.value })}
                            placeholder="2026-07-01"
                          />
                        </FlowField>
                      </div>
                      {assetErrors[index] && (
                        <p className="pt-2 text-xs text-destructive">{assetErrors[index]}</p>
                      )}
                    </div>
                  );
                })}
              </div>
              <div className="space-y-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="space-y-1">
                    <h4 className="text-xs font-medium">Known issues</h4>
                    <p className="max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                      Prior-audit findings, acknowledged README issues, and bot output the program
                      already knows about. They and their duplicates are not reportable.
                    </p>
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={addKnownIssue}
                    disabled={draft.knownIssues.length >= 256}
                  >
                    <Plus data-icon="inline-start" />
                    Add known issue
                  </Button>
                </div>
                {draft.knownIssues.map((issue, index) => {
                  const fieldID = `program-known-issue-${issue.id}`;
                  return (
                    <div key={issue.id} className="rounded-xl border border-border/70 bg-muted/20 p-4">
                      <div className="mb-3 flex items-center justify-between gap-3">
                        <p className="text-xs font-medium">Known issue {index + 1}</p>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Remove known issue ${index + 1}`}
                          onClick={() => removeKnownIssue(index)}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                      <div className="grid gap-4 sm:grid-cols-2">
                        <FlowField id={`${fieldID}-source`} label="Source" required>
                          <Input
                            id={`${fieldID}-source`}
                            maxLength={200}
                            value={issue.source}
                            onChange={(event) => updateKnownIssue(index, { source: event.target.value })}
                            placeholder="Prior audit (Trail of Bits, 2025)"
                          />
                        </FlowField>
                        <FlowField id={`${fieldID}-reference`} label="Reference">
                          <Input
                            id={`${fieldID}-reference`}
                            type="url"
                            maxLength={2048}
                            value={issue.reference}
                            onChange={(event) => updateKnownIssue(index, { reference: event.target.value })}
                            placeholder="https://acme.example/audit.pdf"
                            className="font-mono"
                          />
                        </FlowField>
                        <FlowField id={`${fieldID}-summary`} label="Summary" required className="sm:col-span-2">
                          <Textarea
                            id={`${fieldID}-summary`}
                            value={issue.summary}
                            onChange={(event) => updateKnownIssue(index, { summary: event.target.value })}
                            className="min-h-16"
                            placeholder="Rounding loss on withdrawal acknowledged in the README."
                          />
                        </FlowField>
                      </div>
                      {knownIssueErrors[index] && (
                        <p className="pt-2 text-xs text-destructive">{knownIssueErrors[index]}</p>
                      )}
                    </div>
                  );
                })}
              </div>
              <div className="space-y-3">
                <div className="space-y-1">
                  <h4 className="text-xs font-medium">Submission budget</h4>
                  <p className="max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                    Submission is rationed on most platforms, so volume is a liability. Leave the cap
                    empty when the program publishes no explicit limit.
                  </p>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="program-budget-max" label="Reports per period">
                    <Input
                      id="program-budget-max"
                      type="number"
                      min={0}
                      max={10000}
                      step={1}
                      value={draft.budgetMaxPerPeriod}
                      onChange={(event) => update("budgetMaxPerPeriod", event.target.value)}
                      aria-invalid={budgetCapInvalid || budgetCapMissing}
                    />
                    {budgetCapInvalid && (
                      <p className="pt-1 text-xs text-destructive">Enter a whole number from 0 to 10,000.</p>
                    )}
                    {budgetCapMissing && (
                      <p className="pt-1 text-xs text-destructive">A period length needs a report cap.</p>
                    )}
                  </FlowField>
                  <FlowField id="program-budget-period" label="Period (days)" hint="Leave empty when the cap applies per engagement.">
                    <Input
                      id="program-budget-period"
                      type="number"
                      min={0}
                      max={365}
                      step={1}
                      value={draft.budgetPeriodDays}
                      onChange={(event) => update("budgetPeriodDays", event.target.value)}
                      aria-invalid={budgetPeriodInvalid}
                    />
                    {budgetPeriodInvalid && (
                      <p className="pt-1 text-xs text-destructive">Enter a whole number of days from 0 to 365.</p>
                    )}
                  </FlowField>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <div className="flex items-center justify-between gap-3 rounded-lg border border-border/60 bg-background px-3 py-2">
                    <div className="space-y-0.5">
                      <Label htmlFor="program-budget-reputation" className="text-[12.5px]">Cap lifts with reputation</Label>
                      <p className="text-[10px] text-muted-foreground">The program only lifts the cap above a reputation or identity threshold.</p>
                    </div>
                    <Switch
                      id="program-budget-reputation"
                      checked={draft.budgetUnrestrictedRequiresReputation}
                      onCheckedChange={(value) => update("budgetUnrestrictedRequiresReputation", value)}
                    />
                  </div>
                  <div className="flex items-center justify-between gap-3 rounded-lg border border-border/60 bg-background px-3 py-2">
                    <div className="space-y-0.5">
                      <Label htmlFor="program-kyc-required" className="text-[12.5px]">KYC required</Label>
                      <p className="text-[10px] text-muted-foreground">The program verifies identity before it pays a reward.</p>
                    </div>
                    <Switch
                      id="program-kyc-required"
                      checked={draft.kycRequired}
                      onCheckedChange={(kycRequired) => update("kycRequired", kycRequired)}
                    />
                  </div>
                </div>
              </div>
            </section>
            <section className="space-y-3 border-y border-border/60 py-4" aria-labelledby="scan-targets-heading">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="space-y-1">
                  <h3 id="scan-targets-heading" className="text-sm font-medium">Scan targets</h3>
                  <p className="max-w-xl text-[11px] leading-relaxed text-muted-foreground">
                    One program can cover multiple repositories and websites. Each target becomes an
                    independently importable scan configuration with its own workflow.
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
                  Add target
                </Button>
              </div>
              {draft.scanTargets.length === 0 ? (
                <div className="rounded-lg border border-dashed border-border/70 px-4 py-5 text-center text-xs text-muted-foreground">
                  No scan targets. The verified scope can still be saved without an import shortcut.
                </div>
              ) : (
                <div className="space-y-3">
                  {draft.scanTargets.map((target, index) => {
                    const repositoryUrl = target.repositoryUrl.trim();
                    const targetUrl = target.targetUrl.trim();
                    const scanName = target.scanName.trim();
                    const repositoryUrlInvalid = repositoryUrl !== "" &&
                      (repositoryUrl.length > 2048 || !isHttpsUrl(repositoryUrl));
                    const targetUrlInvalid = targetUrl !== "" &&
                      (targetUrl.length > 2048 || !isHttpUrlOrDomain(targetUrl));
                    const scanNameInvalid = scanName !== "" && !isDNS1123Subdomain(scanName);
                    const duplicateScanName = scanName !== "" && (scanNameCounts.get(scanName) ?? 0) > 1;
                    const priorityInvalid = target.priority !== "" && !validPriority(target.priority);
                    const fieldID = `program-target-${target.id}`;
                    return (
                      <div key={target.id} className="rounded-xl border border-border/70 bg-muted/20 p-4">
                        <div className="mb-4 flex items-center justify-between gap-3">
                          <div className="min-w-0">
                            <p className="text-xs font-medium">Target {index + 1}</p>
                            <p className="truncate font-mono text-[10px] text-muted-foreground">
                              {scanName || targetUrl || repositoryUrl || "Unconfigured target"}
                            </p>
                          </div>
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Remove target ${index + 1}`}
                            onClick={() => removeScanTarget(index)}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                        <div className="grid gap-4 sm:grid-cols-2">
                          <FlowField id={`${fieldID}-target-type`} label="Target type" required>
                            <select
                              id={`${fieldID}-target-type`}
                              className={selectClass}
                              value={target.targetType}
                              onChange={(event) => updateScanTarget(index, {
                                targetType: event.target.value as ScanTargetDraft["targetType"],
                              })}
                            >
                              <option value="repository">Repository</option>
                              <option value="website">Website or API</option>
                            </select>
                          </FlowField>
                          <div />
                          {target.targetType === "repository" ? (
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
                          ) : (
                            <FlowField
                              id={`${fieldID}-target-url`}
                              label="Website or API URL"
                              required
                              className="sm:col-span-2"
                            >
                              <Input
                                id={`${fieldID}-target-url`}
                                required
                                maxLength={2048}
                                value={target.targetUrl}
                                onChange={(event) => updateScanTarget(index, { targetUrl: event.target.value })}
                                placeholder="https://api.example.com"
                                className="font-mono"
                                aria-invalid={targetUrlInvalid}
                              />
                              {targetUrlInvalid && (
                                <p className="pt-1 text-xs text-destructive">Enter an HTTP(S) URL or bare domain without user information.</p>
                              )}
                            </FlowField>
                          )}
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
                          {target.targetType === "repository" && (
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
                          )}
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
                              <p className="text-[10px] text-muted-foreground">Highlight this target in target pickers.</p>
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
