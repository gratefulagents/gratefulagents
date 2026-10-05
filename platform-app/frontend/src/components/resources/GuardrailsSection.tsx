import { create } from "@bufbuild/protobuf";
import { useCallback, useMemo, useState } from "react";
import { Plus, ShieldCheck, Trash2 } from "lucide-react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { resourceNameError } from "@/lib/resourceNames";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { filterByQuery } from "@/components/ui/list-search";
import { toast } from "@/components/ui/toaster";
import { FlowField } from "@/components/create-flow/create-flow";
import { GuardrailPolicySchema, GuardrailRuleSchema, type GuardrailPolicy } from "@/rpc/platform/service_pb";
import { ResourceFormDialog } from "@/components/resources/ResourceFormDialog";
import { Fact, Pill, ResourceRow, ResourceSection } from "@/components/resources/ResourceSection";
import { ChoiceCards, FieldError, FieldGrid, FormSection, NameField } from "@/components/resources/form-kit";
import { joinFacts, regexError, resourceMeta } from "@/components/resources/resource-helpers";
import { useResourceList } from "@/components/resources/use-resource-list";

type RuleDraft = { name: string; type: string; toolPattern: string; regex: string; action: string; message: string };
type Draft = { name: string; rules: RuleDraft[] };

const emptyRule = (): RuleDraft => ({ name: "", type: "tool-input", toolPattern: "*", regex: "", action: "block", message: "" });

function draftFromPolicy(policy: GuardrailPolicy | null): Draft {
  if (!policy) return { name: "", rules: [emptyRule()] };
  const rules = policy.rules.map((rule) => ({
    name: rule.name, type: rule.type || "tool-input", toolPattern: rule.toolPattern, regex: rule.regex, action: rule.action || "block", message: rule.message,
  }));
  return { name: policy.name, rules: rules.length ? rules : [emptyRule()] };
}


const meta = resourceMeta.guardrails;

export function GuardrailsSection() {
  const load = useCallback(async () => (await client.listGuardrailPolicies({})).policies, []);
  const { rows, loading, error, reload } = useResourceList(load);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<GuardrailPolicy | null | undefined>();
  const [deleting, setDeleting] = useState<GuardrailPolicy | null>(null);

  const visible = useMemo(
    () => filterByQuery(rows, query, (policy) => [policy.name, ...policy.rules.flatMap((rule) => [rule.name, rule.toolPattern, rule.regex])]),
    [rows, query],
  );

  async function remove(policy: GuardrailPolicy) {
    await client.deleteGuardrailPolicy({ name: policy.name });
    toast.success(`Deleted ${policy.name}`);
    await reload();
  }

  const createButton = (
    <Button size="sm" onClick={() => setEditing(null)}>
      <Plus data-icon="inline-start" />
      New policy
    </Button>
  );

  return (
    <>
      <ResourceSection
        description={meta.description}
        count={rows.length}
        query={query}
        onQuery={setQuery}
        searchPlaceholder="Search policies and rules"
        actions={createButton}
        loading={loading}
        error={error}
        onRetry={() => void reload()}
        empty={!loading && rows.length === 0}
        noMatches={visible.length === 0 && rows.length > 0}
        emptyIcon={<ShieldCheck />}
        emptyTitle="No guardrail policies yet"
        emptyDescription="Write rules that match tool input or output and decide whether to block, warn, or log. Attach a policy to a project or trigger to enforce it."
        emptyAction={createButton}
      >
        {visible.map((policy) => {
          const counts = policy.rules.reduce<Record<string, number>>((acc, rule) => ({ ...acc, [rule.action]: (acc[rule.action] ?? 0) + 1 }), {});
          const patterns = [...new Set(policy.rules.map((rule) => rule.toolPattern || "*"))];
          return (
            <ResourceRow
              key={policy.name}
              name={policy.name}
              icon={<ShieldCheck />}
              badges={
                <Pill tone="neutral" className="tabular-nums">
                  {policy.rules.length} {policy.rules.length === 1 ? "rule" : "rules"}
                </Pill>
              }
              description={policy.rules.length ? policy.rules.map((rule) => rule.name).join(", ") : "No rules yet — this policy allows everything."}
              facts={
                policy.rules.length > 0 && (
                  <>
                    {(["block", "warn", "log"] as const).filter((action) => counts[action]).map((action) => (
                      <Fact key={action}>
                        <span className={cn("size-1.5 rounded-full", action === "block" ? "bg-[color:var(--tone-danger)]" : action === "warn" ? "bg-[color:var(--tone-warning)]" : "bg-muted-foreground/60")} />
                        {counts[action]} {action}
                      </Fact>
                    ))}
                    <Fact title={patterns.join(", ")}>tools: {patterns.join(", ")}</Fact>
                  </>
                )
              }
              onOpen={() => setEditing(policy)}
              onEdit={() => setEditing(policy)}
              onDelete={() => setDeleting(policy)}
            />
          );
        })}
      </ResourceSection>

      {editing !== undefined && (
        <GuardrailDialog
          policy={editing}
          onClose={() => setEditing(undefined)}
          onSaved={async () => {
            setEditing(undefined);
            await reload();
          }}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete ${deleting?.name ?? ""}?`}
        description="Projects and triggers that reference this policy stop enforcing its rules. This cannot be undone."
        confirmLabel="Delete policy"
        destructive
        onConfirm={async () => {
          if (deleting) await remove(deleting);
        }}
      />
    </>
  );
}

function GuardrailDialog({ policy, onClose, onSaved }: { policy: GuardrailPolicy | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const isNew = policy === null;
  const [initial] = useState(() => draftFromPolicy(policy));
  const [draft, setDraft] = useState<Draft>(initial);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const ruleProblems = draft.rules.map((rule) => ({
    name: rule.name.trim() ? null : "Give the rule a name.",
    regex: rule.regex.trim() ? regexError(rule.regex) : "A pattern is required.",
  }));
  const valid =
    Boolean(draft.name.trim()) &&
    (!isNew || resourceNameError(draft.name.trim()) === null) &&
    ruleProblems.every((problem) => !problem.name && !problem.regex);

  const updateRule = (index: number, patch: Partial<RuleDraft>) =>
    setDraft((current) => ({ ...current, rules: current.rules.map((rule, at) => (at === index ? { ...rule, ...patch } : rule)) }));

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const value = create(GuardrailPolicySchema, {
        name: draft.name.trim(),
        rules: draft.rules.map((rule) =>
          create(GuardrailRuleSchema, { ...rule, name: rule.name.trim(), toolPattern: rule.toolPattern.trim() || "*", message: rule.message.trim() }),
        ),
      });
      await (isNew ? client.createGuardrailPolicy({ policy: value }) : client.updateGuardrailPolicy({ policy: value }));
      toast.success(`${isNew ? "Created" : "Saved"} ${value.name}`);
      await onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  return (
    <ResourceFormDialog
      open
      onClose={onClose}
      icon={<ShieldCheck />}
      title={isNew ? "New guardrail policy" : `Edit ${policy.name}`}
      description="Rules run in order against every tool call that matches their tool pattern. Saving replaces the policy's rule list."
      dirty={dirty}
      saving={saving}
      canSave={valid && (isNew || dirty)}
      saveLabel={isNew ? "Create policy" : "Save changes"}
      error={error}
      onSave={() => void save()}
      footerStart={
        <span className="text-muted-foreground tabular-nums">
          {draft.rules.length} {draft.rules.length === 1 ? "rule" : "rules"}
        </span>
      }
    >
      <FormSection title="Policy">
        <FieldGrid>
          <NameField id="guardrail-name" value={draft.name} onChange={(name) => setDraft({ ...draft, name })} isNew={isNew} placeholder="no-secrets-in-output" autoFocus={isNew} />
        </FieldGrid>
      </FormSection>

      <FormSection
        title="Rules"
        description="Each rule matches a regular expression against the input sent to a tool or the output it returns."
        aside={
          <Button type="button" variant="outline" size="sm" onClick={() => setDraft({ ...draft, rules: [...draft.rules, emptyRule()] })}>
            <Plus data-icon="inline-start" />
            Add rule
          </Button>
        }
      >
        {draft.rules.map((rule, index) => {
          const problems = ruleProblems[index];
          return (
            <div key={index} className="space-y-4 rounded-xl border border-border/60 bg-muted/20 p-4">
              <div className="flex items-center justify-between gap-3">
                <span className="text-[12px] font-medium text-muted-foreground">
                  Rule {index + 1}
                  {rule.name.trim() && <span className="ml-1.5 font-mono text-foreground">{rule.name.trim()}</span>}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={`Remove rule ${index + 1}`}
                  className="text-muted-foreground hover:text-destructive"
                  disabled={draft.rules.length === 1}
                  onClick={() => setDraft({ ...draft, rules: draft.rules.filter((_, at) => at !== index) })}
                >
                  <Trash2 data-icon="inline-start" />
                  Remove
                </Button>
              </div>
              <FieldGrid>
                <FlowField id={`rule-${index}-name`} label="Rule name" required>
                  <Input id={`rule-${index}-name`} value={rule.name} onChange={(event) => updateRule(index, { name: event.target.value })} placeholder="no-rm-rf" className="font-mono" autoComplete="off" aria-invalid={rule.name || !dirty ? undefined : true} />
                </FlowField>
                <FlowField id={`rule-${index}-tool`} label="Tool pattern" hint="Glob over tool names: * for every tool, bash* for shell tools.">
                  <Input id={`rule-${index}-tool`} value={rule.toolPattern} onChange={(event) => updateRule(index, { toolPattern: event.target.value })} placeholder="*" className="font-mono" autoComplete="off" />
                </FlowField>
              </FieldGrid>
              <FieldGrid>
                <div className="space-y-1.5">
                  <span id={`rule-${index}-type-label`} className="text-[12.5px] font-medium">Inspect</span>
                  <ChoiceCards
                    aria-labelledby={`rule-${index}-type-label`}
                    value={rule.type}
                    onChange={(type) => updateRule(index, { type })}
                    columns={2}
                    compact
                    options={[
                      { value: "tool-input", label: "Tool input" },
                      { value: "tool-output", label: "Tool output" },
                    ]}
                  />
                </div>
                <div className="space-y-1.5">
                  <span id={`rule-${index}-action-label`} className="text-[12.5px] font-medium">On match</span>
                  <ChoiceCards
                    aria-labelledby={`rule-${index}-action-label`}
                    value={rule.action}
                    onChange={(action) => updateRule(index, { action })}
                    columns={3}
                    compact
                    options={[
                      { value: "block", label: "Block", tone: "danger" },
                      { value: "warn", label: "Warn", tone: "warning" },
                      { value: "log", label: "Log" },
                    ]}
                  />
                </div>
              </FieldGrid>
              <FlowField id={`rule-${index}-regex`} label="Pattern" required hint="Go regular expression. Blocked calls return the message below to the agent.">
                <Input
                  id={`rule-${index}-regex`}
                  value={rule.regex}
                  onChange={(event) => updateRule(index, { regex: event.target.value })}
                  placeholder="rm\s+-rf\s+/"
                  className="font-mono"
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={rule.regex && problems.regex ? true : undefined}
                />
                {rule.regex && <FieldError>{problems.regex}</FieldError>}
              </FlowField>
              <FlowField id={`rule-${index}-message`} label="Message">
                <Input id={`rule-${index}-message`} value={rule.message} onChange={(event) => updateRule(index, { message: event.target.value })} placeholder="Destructive filesystem commands are not allowed." />
              </FlowField>
            </div>
          );
        })}
        <p className="text-[11.5px] text-muted-foreground">
          {joinFacts([
            `${draft.rules.filter((rule) => rule.action === "block").length} block`,
            `${draft.rules.filter((rule) => rule.action === "warn").length} warn`,
            `${draft.rules.filter((rule) => rule.action === "log").length} log`,
          ])}
        </p>
      </FormSection>
    </ResourceFormDialog>
  );
}
