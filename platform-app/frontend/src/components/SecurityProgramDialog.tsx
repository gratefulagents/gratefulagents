import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";

import { FlowField } from "@/components/create-flow/create-flow";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { client } from "@/lib/client";
import {
  CreateSecurityProgramRequestSchema,
  SecurityProgramResourceSchema,
  UpdateSecurityProgramRequestSchema,
  type SecurityProgramResource,
} from "@/rpc/platform/service_pb";

type ProgramDraft = {
  name: string;
  provider: string;
  displayName: string;
  programUrl: string;
  scopePolicy: string;
  verifiedAt: string;
};

function localDateTimeValue(source?: SecurityProgramResource): string {
  if (!source?.verifiedAt) return "";
  const date = timestampDate(source.verifiedAt);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 19);
}

function programToDraft(source?: SecurityProgramResource): ProgramDraft {
  return {
    name: source?.name ?? "",
    provider: source?.provider ?? "",
    displayName: source?.displayName ?? "",
    programUrl: source?.programUrl ?? "",
    scopePolicy: source?.scopePolicy ?? "",
    verifiedAt: localDateTimeValue(source),
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
  const blocked =
    !draft.name.trim() ||
    !draft.provider.trim() ||
    !draft.displayName.trim() ||
    !draft.programUrl.trim() ||
    urlInvalid ||
    !draft.scopePolicy.trim() ||
    !draft.verifiedAt;

  function update<K extends keyof ProgramDraft>(field: K, value: ProgramDraft[K]) {
    setDraft((current) => ({ ...current, [field]: value }));
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
      <DialogContent className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]" showCloseButton>
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
                maxLength={32768}
                className="min-h-40 font-mono"
                placeholder={"In scope:\n- api.example.com\n\nOut of scope:\n- production denial-of-service testing"}
              />
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
