import { create } from "@bufbuild/protobuf";
import { useState } from "react";
import { CalendarClock, Clock3, Loader2 } from "lucide-react";

import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Chip,
  FlowField,
  FlowSwitchRow,
  OptionRow,
  OptionRows,
  Segmented,
} from "@/components/create-flow/create-flow";
import { RunDefaultsRows } from "@/components/run-defaults/RunDefaultsRows";
import { TriggerPolicyRows } from "@/components/run-defaults/TriggerPolicyRows";
import {
  buildCronRequest,
  cronToDefaults,
  cronUsesSavedCredentials,
  emptyDefaults,
} from "@/components/run-defaults/helpers";
import { resolvedTriggerPolicies } from "@/components/TriggerDefaultsDialog";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import {
  CreateCronRequestSchema,
  UpdateCronRequestSchema,
  type AgentRunDefaults,
  type Cron,
  type TriggerPolicies,
} from "@/rpc/platform/service_pb";

const SCHEDULE_PRESETS = ["@hourly", "@daily", "@weekly", "0 9 * * 1-5"];

type SpecState = {
  name: string;
  schedule: string;
  timeZone: string;
  concurrencyPolicy: string;
  suspend: boolean;
  prompt: string;
};

function initialSpec(cron?: Cron): SpecState {
  return {
    name: cron?.name ?? "",
    schedule: cron?.schedule ?? "",
    timeZone: cron?.timeZone ?? "",
    concurrencyPolicy: cron?.concurrencyPolicy || "Forbid",
    suspend: cron?.suspend ?? false,
    prompt: cron?.prompt ?? "",
  };
}

function scheduleSummary(spec: SpecState): string {
  const parts = [spec.timeZone.trim() || "UTC"];
  parts.push(spec.concurrencyPolicy === "Allow" ? "overlaps allowed" : "overlaps skipped");
  if (spec.suspend) parts.push("paused");
  return parts.join(" · ");
}

/**
 * Create/edit dialog for Cron triggers. Pass `cron` to edit an existing
 * trigger; omit it to create a new one.
 */
export function CronFormDialog({
  cron,
  trigger,
  defaultOpen = false,
  onSaved,
}: {
  cron?: Cron;
  trigger: React.ReactElement;
  defaultOpen?: boolean;
  onSaved?: (cron: Cron) => void;
}) {
  const isEdit = Boolean(cron);
  const [open, setOpen] = useState(defaultOpen);
  const [spec, setSpec] = useState<SpecState>(() => initialSpec(cron));
  const [defaults, setDefaults] = useState<AgentRunDefaults>(() =>
    cron ? cronToDefaults(cron) : emptyDefaults(),
  );
  const [policies, setPolicies] = useState<TriggerPolicies>(() => resolvedTriggerPolicies(cron));
  const [useSavedCredentials, setUseSavedCredentials] = useState(() =>
    cron ? cronUsesSavedCredentials(cron) : true,
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function update<K extends keyof SpecState>(field: K, value: SpecState[K]) {
    setSpec((prev) => ({ ...prev, [field]: value }));
  }

  function reset() {
    setSpec(initialSpec(cron));
    setDefaults(cron ? cronToDefaults(cron) : emptyDefaults());
    setPolicies(resolvedTriggerPolicies(cron));
    setUseSavedCredentials(cron ? cronUsesSavedCredentials(cron) : true);
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    if (!spec.schedule.trim()) {
      setError("Give the cron a schedule, e.g. @hourly or 0 9 * * 1-5.");
      return;
    }
    if (!spec.prompt.trim()) {
      setError("Give the cron a prompt to run on each schedule.");
      return;
    }
    setSubmitting(true);
    try {
      const request = buildCronRequest({
        namespace: cron?.namespace ?? "",
        name: spec.name,
        schedule: spec.schedule,
        timeZone: spec.timeZone,
        suspend: spec.suspend,
        concurrencyPolicy: spec.concurrencyPolicy,
        prompt: spec.prompt,
        defaults,
        useSavedCredentials,
      });
      const saved = isEdit
        ? await client.updateCron(create(UpdateCronRequestSchema, { ...request, policies }))
        : await client.createCron(create(CreateCronRequestSchema, { ...request, policies }));
      setOpen(false);
      reset();
      onSaved?.(saved);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save cron trigger");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Clock3 className="size-4" />
              </span>
              <DialogTitle className="text-base">
                {isEdit ? `Edit ${cron?.name}` : "New cron trigger"}
              </DialogTitle>
            </div>
            <DialogDescription>
              {isEdit
                ? "Saving replaces the cron's spec with the values below."
                : "Schedule a prompt to launch agent runs automatically."}
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            {/* Essentials — everything else has good defaults. */}
            <FlowField id="cron-prompt" label="Prompt" required>
              <Textarea
                id="cron-prompt"
                value={spec.prompt}
                onChange={(event) => update("prompt", event.target.value)}
                className="min-h-24"
                placeholder="What should the agent do on each run?"
                autoFocus
                required
              />
            </FlowField>

            <FlowField id="cron-schedule" label="Schedule" required>
              <Input
                id="cron-schedule"
                value={spec.schedule}
                onChange={(event) => update("schedule", event.target.value)}
                placeholder="0 9 * * 1-5"
                className="font-mono"
                required
              />
              <div className="flex flex-wrap gap-1.5 pt-1.5">
                {SCHEDULE_PRESETS.map((preset) => (
                  <Chip
                    key={preset}
                    mono
                    selected={spec.schedule === preset}
                    onSelect={() => update("schedule", preset)}
                  >
                    {preset}
                  </Chip>
                ))}
              </div>
            </FlowField>

            {!isEdit ? (
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField
                  id="cron-name"
                  label="Name"
                  hint="Optional — derived from the schedule if empty."
                >
                  <Input
                    id="cron-name"
                    value={spec.name}
                    onChange={(event) => update("name", event.target.value)}
                    placeholder="nightly-report"
                  />
                </FlowField>
              </div>
            ) : null}

            <OptionRows label="Options" className="pt-1">
              <OptionRow
                icon={CalendarClock}
                title="Scheduling"
                summary={scheduleSummary(spec)}
                modified={Boolean(spec.timeZone.trim()) || spec.concurrencyPolicy === "Allow" || spec.suspend}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="cron-time-zone"
                    label="Time zone"
                    hint="IANA name, empty = UTC."
                  >
                    <Input
                      id="cron-time-zone"
                      value={spec.timeZone}
                      onChange={(event) => update("timeZone", event.target.value)}
                      placeholder="America/New_York"
                    />
                  </FlowField>
                </div>
                <FlowSwitchRow
                  label="If the previous run is still active"
                  control={
                    <Segmented
                      aria-label="Concurrency policy"
                      value={spec.concurrencyPolicy === "Allow" ? "Allow" : "Forbid"}
                      onChange={(policy) => update("concurrencyPolicy", policy)}
                      options={[
                        { value: "Forbid", label: "Skip" },
                        { value: "Allow", label: "Run anyway" },
                      ]}
                    />
                  }
                />
                <FlowSwitchRow
                  id="cron-suspend"
                  label="Suspend"
                  hint="Pause scheduling without deleting the trigger."
                  control={
                    <Switch
                      id="cron-suspend"
                      checked={spec.suspend}
                      onCheckedChange={(checked) => update("suspend", checked)}
                    />
                  }
                />
              </OptionRow>

              <RunDefaultsRows
                idPrefix="cron-defaults"
                resourceNamespace={cron?.namespace}
                value={defaults}
                onChange={setDefaults}
                useSavedCredentials={useSavedCredentials}
                onUseSavedCredentialsChange={setUseSavedCredentials}
              />
              <TriggerPolicyRows
                idPrefix="cron-policies"
                policies={policies}
                onPoliciesChange={setPolicies}
                runtimeProfileRef={defaults.runtimeProfileRef}
                onRuntimeProfileRefChange={(ref) =>
                  setDefaults((prev) => ({ ...prev, runtimeProfileRef: ref }))
                }
                mcpPolicyRef={defaults.mcpPolicyRef}
                onMcpPolicyRefChange={(ref) =>
                  setDefaults((prev) => ({ ...prev, mcpPolicyRef: ref }))
                }
              />
            </OptionRows>

            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>

          <div className="flex items-center justify-end gap-2 border-t px-6 py-4">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting
                ? isEdit
                  ? "Saving…"
                  : "Creating…"
                : isEdit
                  ? "Save changes"
                  : "Create cron"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
