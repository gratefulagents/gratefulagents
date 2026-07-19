import { create } from "@bufbuild/protobuf";
import { useCallback, useEffect, useState } from "react";
import { Link, Navigate, useParams } from "react-router-dom";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { useAuth } from "@/contexts/AuthContext";
import { client } from "@/lib/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { MCPServersSection } from "@/components/MCPServersSection";
import { SkillsSection } from "@/components/SkillsSection";
import { RuntimeProfileSchema, MCPPolicySchema, MCPAllowedServerSchema, MCPBreakGlassSchema, GuardrailPolicySchema, GuardrailRuleSchema, ModeConstraintsSchema, ModeTemplateSchema, RoleInstructionSchema } from "@/rpc/platform/service_pb";
import { canMutateResource, formatProviderModels, parseProviderModels, resourceTabs, type ResourceKind } from "@/components/resources/resource-helpers";
import { parseStringList, parseStringMap, runtimeProfileFormFromRow } from "@/components/resources/runtime-profile-form";

type Row = { name: string; namespace?: string; [key: string]: unknown };
type Form = Record<string, string | boolean>;
type GuardrailRuleDraft = { name: string; type: string; toolPattern: string; regex: string; action: string; message: string };
const emptyGuardrailRule = (): GuardrailRuleDraft => ({ name: "", type: "tool-input", toolPattern: "*", regex: "", action: "block", message: "" });
const descriptions: Record<ResourceKind, string> = {
  skills: "Reusable instructions and packages agents can load.", "mcp-servers": "Tool servers agents can connect to.",
  "runtime-profiles": "Runtime permissions, network access, and workspace defaults.", "mcp-policies": "Control which MCP servers and tools runs may use.",
  guardrails: "Inspect tool input and output with enforceable rules.", modes: "Behavior and execution templates available to agents.",
  roles: "Reusable role instructions and tool-access boundaries.",
};
const initial: Record<string, Form> = {
  "runtime-profiles": {
    name: "", permissionMode: "workspace-write", egressMode: "restricted", defaultTimeout: "",
    sandboxTemplateRef: "", runtimeClassName: "", warmPoolRef: "", persistWorkspace: false,
    workspaceSize: "", enablePrivateProcfs: false, commandPath: "", commandPathPrepend: "",
    commandPathAppend: "", extraReadOnlyPaths: "", extraWritablePaths: "", commandEnv: "",
    resourceRequests: "", resourceLimits: "", maxConcurrentRuns: "0",
    perNamespaceMaxConcurrentRuns: "0", staleRunTimeout: "",
  },
  "mcp-policies": { name: "", defaultAction: "Deny", allowedServers: "", manageBreakGlass: false, breakGlassEnabled: false, requireAuditReason: false, adminMediated: false },
  guardrails: { name: "" },
  modes: { name: "", version: "v1", displayName: "", description: "", category: "direct", executionStrategy: "serial", instructions: "", autonomous: false, permissionMode: "workspace-write", allowedMutatingTools: "", defaultMcpServerRefs: "", defaultSkillRefs: "", manageConstraints: false, maxTurns: "", subagentMaxTurns: "", maxRuntimeMinutes: "", maxRetries: "", maxConcurrentSubagents: "" },
  roles: { name: "", description: "", instructions: "", toolAccess: "full", model: "", providerModels: "", reasoningLevel: "" },
};
const csv = (v: string) => v.split(",").map((x) => x.trim()).filter(Boolean);

function formFromRow(kind: ResourceKind, row: Row): Form {
  if (kind === "runtime-profiles") return runtimeProfileFormFromRow(row);
  if (kind === "mcp-policies") {
    const breakGlass = row.breakGlass as { enabled?: boolean; requireAuditReason?: boolean; adminMediated?: boolean } | undefined;
    return {
      name: row.name,
      defaultAction: String(row.defaultAction),
      allowedServers: (row.allowedServers as Array<{name:string; tools:string[]}>).map((s) => `${s.name}${s.tools.length ? `:${s.tools.join("|")}` : ""}`).join(", "),
      manageBreakGlass: Boolean(breakGlass),
      breakGlassEnabled: Boolean(breakGlass?.enabled),
      requireAuditReason: Boolean(breakGlass?.requireAuditReason),
      adminMediated: Boolean(breakGlass?.adminMediated),
    };
  }
  if (kind === "guardrails") return { name: row.name };
  if (kind === "modes") {
    const constraints = row.constraints as { maxTurns?: number; subagentMaxTurns?: number; maxRuntimeMinutes?: number; maxRetries?: number; maxConcurrentSubagents?: number } | undefined;
    return { name: row.name, version: String(row.version), displayName: String(row.displayName), description: String(row.description), category: String(row.category), executionStrategy: String(row.executionStrategy), instructions: String(row.instructions), autonomous: Boolean(row.autonomous), permissionMode: String(row.permissionMode), allowedMutatingTools: (row.allowedMutatingTools as string[]).join(", "), defaultMcpServerRefs: (row.defaultMcpServerRefs as string[]).join(", "), defaultSkillRefs: (row.defaultSkillRefs as string[]).join(", "), manageConstraints: Boolean(constraints), maxTurns: constraints ? String(constraints.maxTurns ?? 0) : "", subagentMaxTurns: constraints ? String(constraints.subagentMaxTurns ?? 0) : "", maxRuntimeMinutes: constraints ? String(constraints.maxRuntimeMinutes ?? 0) : "", maxRetries: constraints ? String(constraints.maxRetries ?? 0) : "", maxConcurrentSubagents: constraints ? String(constraints.maxConcurrentSubagents ?? 0) : "" };
  }
  return { name: row.name, description: String(row.description), instructions: String(row.instructions), toolAccess: String(row.toolAccess), model: String(row.model ?? ""), providerModels: formatProviderModels(row.modelsByProvider as Record<string, string> | undefined), reasoningLevel: String(row.reasoningLevel ?? "") };
}

export function ResourcePage() {
  const rawKind = useParams().kind;
  const { user } = useAuth();
  if (!resourceTabs.some(([id]) => id === rawKind)) {
    return <Navigate to="/resources/skills" replace />;
  }
  const kind = rawKind as ResourceKind;
  const mutable = canMutateResource(kind, user?.role);
  return <div className="space-y-5"><header><h1 className="text-[22px] font-semibold">Resources</h1><p className="text-[13px] text-muted-foreground">Configure reusable building blocks for agents and runs.</p></header>
    <nav aria-label="Resource types" className="flex gap-1 overflow-x-auto border-b">{resourceTabs.map(([id,label]) => <Link key={id} to={`/resources/${id}`} className={`whitespace-nowrap border-b-2 px-3 py-2 text-sm ${kind === id ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"}`}>{label}</Link>)}</nav>
    {kind === "skills" ? <SkillsSection /> : kind === "mcp-servers" ? <MCPServersSection /> : <ManagedResources kind={kind} mutable={mutable} />}
  </div>;
}

function ManagedResources({ kind, mutable }: { kind: Exclude<ResourceKind,"skills"|"mcp-servers">; mutable: boolean }) {
  const [rows,setRows] = useState<Row[]>([]), [loading,setLoading] = useState(true), [error,setError] = useState<string|null>(null);
  const [editing,setEditing] = useState<Row|null|undefined>(), [deleting,setDeleting] = useState<Row|null>(null);
  const load = useCallback(async () => { setLoading(true); setError(null); try {
    if (kind === "runtime-profiles") setRows((await client.listRuntimeProfiles({})).profiles);
    else if (kind === "mcp-policies") setRows((await client.listMCPPolicies({})).policies);
    else if (kind === "guardrails") setRows((await client.listGuardrailPolicies({})).policies);
    else if (kind === "modes") setRows((await client.listModeTemplates({})).templates);
    else setRows((await client.listRoleInstructions({})).instructions);
  } catch(e) { setError(e instanceof Error ? e.message : String(e)); } finally { setLoading(false); } }, [kind]);
  useEffect(() => { const timer = window.setTimeout(() => void load(), 0); return () => window.clearTimeout(timer); }, [load]);
  async function remove(row: Row) {
    if (kind === "runtime-profiles") await client.deleteRuntimeProfile({name:row.name});
    else if (kind === "mcp-policies") await client.deleteMCPPolicy({name:row.name});
    else if (kind === "guardrails") await client.deleteGuardrailPolicy({name:row.name});
    else if (kind === "modes") await client.deleteModeTemplate({name:row.name});
    else await client.deleteRoleInstruction({name:row.name});
    await load();
  }
  return <section className="space-y-4"><div className="flex items-start justify-between gap-3"><div><h2 className="text-lg font-semibold">{resourceTabs.find(([id])=>id===kind)?.[1]}</h2><p className="text-sm text-muted-foreground">{descriptions[kind]}</p>{!mutable && <p className="mt-1 text-xs text-muted-foreground">Only administrators can change these resources.</p>}</div>{mutable && <Button onClick={()=>setEditing(null)}><Plus className="size-4"/>Create</Button>}</div>
    {loading ? <p className="text-sm text-muted-foreground">Loading…</p> : error ? <div className="text-sm text-destructive">{error} <Button variant="outline" size="sm" onClick={()=>void load()}>Retry</Button></div> : rows.length === 0 ? <div className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">No resources found.{mutable && <div className="mt-3"><Button variant="outline" onClick={()=>setEditing(null)}>Create the first one</Button></div>}</div> : <div className="divide-y rounded-lg border">{rows.map(row=><div key={row.name} className="flex items-start justify-between gap-4 p-4"><div className="min-w-0"><div className="font-medium">{String(row.displayName || row.name)}</div><div className="font-mono text-xs text-muted-foreground">{row.namespace ? `${row.namespace}/` : ""}{row.name}</div><div className="mt-1 line-clamp-2 text-sm text-muted-foreground">{summary(kind,row)}</div></div>{mutable && <div className="flex shrink-0 gap-1"><Button variant="ghost" size="icon" aria-label={`Edit ${row.name}`} onClick={()=>setEditing(row)}><Pencil className="size-4"/></Button><Button variant="ghost" size="icon" aria-label={`Delete ${row.name}`} onClick={()=>setDeleting(row)}><Trash2 className="size-4"/></Button></div>}</div>)}</div>}
    {editing !== undefined && <ResourceDialog kind={kind} row={editing} onClose={()=>setEditing(undefined)} onSaved={async()=>{setEditing(undefined);await load();}}/>}
    <ConfirmDialog open={Boolean(deleting)} onOpenChange={(o)=>!o&&setDeleting(null)} title={`Delete ${deleting?.name}?`} description="This cannot be undone." confirmLabel="Delete" destructive onConfirm={async()=>{if(deleting) await remove(deleting);}} />
  </section>;
}
function summary(kind: ResourceKind,row: Row) {
  if (kind === "runtime-profiles") return `${row.permissionMode} · ${row.egressMode} egress${row.enablePrivateProcfs ? " · private procfs" : ""}${row.defaultTimeout ? ` · ${row.defaultTimeout}`:""}`;
  if (kind === "mcp-policies") return `${row.defaultAction} by default · ${(row.allowedServers as unknown[])?.length || 0} allowed servers`;
  if (kind === "guardrails") return `${(row.rules as unknown[])?.length || 0} rules`;
  if (kind === "modes") return `${row.category} · ${row.executionStrategy} · ${row.description || "No description"}`;
  const mappedModelCount = Object.keys((row.modelsByProvider as Record<string, string> | undefined) ?? {}).length;
  const modelSummary = [row.model ? `legacy: ${row.model}` : "", mappedModelCount ? `${mappedModelCount} provider model${mappedModelCount === 1 ? "" : "s"}` : "", row.reasoningLevel ? `${row.reasoningLevel} reasoning` : ""].filter(Boolean).join(" · ");
  return `${row.toolAccess} tools · ${modelSummary || row.description || "No description"}`;
}

function ResourceDialog({kind,row,onClose,onSaved}:{kind:Exclude<ResourceKind,"skills"|"mcp-servers">;row:Row|null;onClose:()=>void;onSaved:()=>void}) {
  const [form, setForm] = useState<Form>(() => row ? formFromRow(kind, row) : { ...initial[kind] });
  const [guardrailRules, setGuardrailRules] = useState<GuardrailRuleDraft[]>(() => {
    if (kind !== "guardrails" || !row) return [emptyGuardrailRule()];
    const rules = (row.rules as GuardrailRuleDraft[] | undefined) ?? [];
    return rules.length ? rules.map((rule) => ({ ...rule })) : [emptyGuardrailRule()];
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = (key:string, value:string|boolean) => setForm((current) => ({ ...current, [key]: value }));

  async function save() {
    setSaving(true);
    setError(null);
    try {
      if (kind === "runtime-profiles") {
        const value = create(RuntimeProfileSchema, {
          name:String(form.name), permissionMode:String(form.permissionMode), egressMode:String(form.egressMode),
          defaultTimeout:String(form.defaultTimeout), sandboxTemplateRef:String(form.sandboxTemplateRef),
          runtimeClassName:String(form.runtimeClassName), warmPoolRef:String(form.warmPoolRef),
          persistWorkspace:Boolean(form.persistWorkspace), workspaceSize:String(form.workspaceSize),
          enablePrivateProcfs:Boolean(form.enablePrivateProcfs), commandPath:parseStringList(String(form.commandPath)),
          commandPathPrepend:parseStringList(String(form.commandPathPrepend)), commandPathAppend:parseStringList(String(form.commandPathAppend)),
          extraReadOnlyPaths:parseStringList(String(form.extraReadOnlyPaths)), extraWritablePaths:parseStringList(String(form.extraWritablePaths)),
          commandEnv:parseStringMap(String(form.commandEnv), "Command environment"),
          resourceRequests:parseStringMap(String(form.resourceRequests), "Resource requests"),
          resourceLimits:parseStringMap(String(form.resourceLimits), "Resource limits"),
          maxConcurrentRuns:Number(form.maxConcurrentRuns) || 0,
          perNamespaceMaxConcurrentRuns:Number(form.perNamespaceMaxConcurrentRuns) || 0,
          staleRunTimeout:String(form.staleRunTimeout), replaceSpec:true,
        });
        await (row ? client.updateRuntimeProfile({profile:value}) : client.createRuntimeProfile({profile:value}));
      } else if (kind === "mcp-policies") {
        const allowedServers = csv(String(form.allowedServers)).map((entry) => { const [name,tools=""] = entry.split(":"); return create(MCPAllowedServerSchema, { name, tools:tools.split("|").filter(Boolean) }); });
        const breakGlass = form.manageBreakGlass ? create(MCPBreakGlassSchema, { enabled:Boolean(form.breakGlassEnabled), requireAuditReason:Boolean(form.requireAuditReason), adminMediated:Boolean(form.adminMediated) }) : undefined;
        const value = create(MCPPolicySchema, { name:String(form.name), defaultAction:String(form.defaultAction), allowedServers, breakGlass, replaceSpec:true });
        await (row ? client.updateMCPPolicy({policy:value}) : client.createMCPPolicy({policy:value}));
      } else if (kind === "guardrails") {
        const rules = guardrailRules
          .filter((rule) => rule.name.trim() || rule.regex.trim())
          .map((rule) => create(GuardrailRuleSchema, rule));
        const value = create(GuardrailPolicySchema, { name:String(form.name), rules });
        await (row ? client.updateGuardrailPolicy({policy:value}) : client.createGuardrailPolicy({policy:value}));
      } else if (kind === "modes") {
        const constraints = form.manageConstraints ? create(ModeConstraintsSchema, { maxTurns:Number(form.maxTurns) || 0, subagentMaxTurns:Number(form.subagentMaxTurns) || 0, maxRuntimeMinutes:Number(form.maxRuntimeMinutes) || 0, maxRetries:Number(form.maxRetries) || 0, maxConcurrentSubagents:Number(form.maxConcurrentSubagents) || 0 }) : undefined;
        const value = create(ModeTemplateSchema, { name:String(form.name), version:String(form.version), displayName:String(form.displayName), description:String(form.description), category:String(form.category), executionStrategy:String(form.executionStrategy), instructions:String(form.instructions), autonomous:Boolean(form.autonomous), permissionMode:String(form.permissionMode), allowedMutatingTools:csv(String(form.allowedMutatingTools)), defaultMcpServerRefs:csv(String(form.defaultMcpServerRefs)), defaultSkillRefs:csv(String(form.defaultSkillRefs)), constraints });
        await (row ? client.updateModeTemplate({template:value}) : client.createModeTemplate({template:value}));
      } else {
        const modelsByProvider = parseProviderModels(String(form.providerModels));
        const value = create(RoleInstructionSchema, { name:String(form.name), description:String(form.description), instructions:String(form.instructions), toolAccess:String(form.toolAccess), model:String(form.model).trim(), modelsByProvider, reasoningLevel:String(form.reasoningLevel).trim() });
        await (row ? client.updateRoleInstruction({instruction:value}) : client.createRoleInstruction({instruction:value}));
      }
      onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  const modeConstraintFields = ["maxTurns", "subagentMaxTurns", "maxRuntimeMinutes", "maxRetries", "maxConcurrentSubagents"];
  const fields = Object.keys(initial[kind]).filter((key) => kind !== "modes" || Boolean(form.manageConstraints) || !modeConstraintFields.includes(key));
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{row ? "Edit" : "Create"} {resourceTabs.find(([id]) => id === kind)?.[1].replace(/s$/, "")}</DialogTitle>
          <DialogDescription>{descriptions[kind]}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 sm:grid-cols-2">
          {fields.map((key) => <Field key={key} name={key} value={form[key]} set={set} disabled={Boolean(row && key === "name")} wide={["instructions","description","commandPath","commandPathPrepend","commandPathAppend","extraReadOnlyPaths","extraWritablePaths","commandEnv","resourceRequests","resourceLimits","allowedServers","allowedMutatingTools","defaultMcpServerRefs","defaultSkillRefs","providerModels"].includes(key)} />)}
        </div>
        {kind === "guardrails" && <GuardrailRulesEditor rules={guardrailRules} onChange={setGuardrailRules} />}
        {error && <p className="text-sm text-destructive">{error}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button disabled={saving || !String(form.name).trim()} onClick={() => void save()}>{saving ? "Saving…" : "Save"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function GuardrailRulesEditor({ rules, onChange }: { rules: GuardrailRuleDraft[]; onChange: (rules: GuardrailRuleDraft[]) => void }) {
  const update = (index: number, key: keyof GuardrailRuleDraft, value: string) => onChange(rules.map((rule, current) => current === index ? { ...rule, [key]: value } : rule));
  return (
    <fieldset className="space-y-3 rounded-lg border p-3">
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium">Rules</span>
        <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rules, emptyGuardrailRule()])}><Plus className="size-4" />Add rule</Button>
      </div>
      {rules.map((rule, index) => (
        <div key={index} className="grid gap-3 rounded-md bg-muted/30 p-3 sm:grid-cols-2">
          <Field name={`rule-${index}-name`} label="Name" value={rule.name} set={(_, value) => update(index, "name", String(value))} />
          <Field name={`rule-${index}-tool-pattern`} label="Tool pattern" value={rule.toolPattern} set={(_, value) => update(index, "toolPattern", String(value))} />
          <div className="space-y-1.5"><Label>Type</Label><select className="h-9 w-full rounded-md border bg-background px-3 text-sm" value={rule.type} onChange={(event) => update(index, "type", event.target.value)}><option value="tool-input">Tool input</option><option value="tool-output">Tool output</option></select></div>
          <div className="space-y-1.5"><Label>Action</Label><select className="h-9 w-full rounded-md border bg-background px-3 text-sm" value={rule.action} onChange={(event) => update(index, "action", event.target.value)}><option value="block">Block</option><option value="warn">Warn</option><option value="log">Log</option></select></div>
          <Field name={`rule-${index}-regex`} label="Regular expression" value={rule.regex} set={(_, value) => update(index, "regex", String(value))} wide />
          <Field name={`rule-${index}-message`} label="Message" value={rule.message} set={(_, value) => update(index, "message", String(value))} wide />
          <div className="sm:col-span-2 flex justify-end"><Button type="button" variant="ghost" size="sm" disabled={rules.length === 1} onClick={() => onChange(rules.filter((_, current) => current !== index))}><Trash2 className="size-4" />Remove rule</Button></div>
        </div>
      ))}
    </fieldset>
  );
}

function Field({name,label,value,set,disabled,wide}:{name:string;label?:string;value:string|boolean;set:(k:string,v:string|boolean)=>void;disabled?:boolean;wide?:boolean}) {
  const displayLabel = label ?? name.replace(/([A-Z])/g," $1").replace(/^./, (character) => character.toUpperCase());
  if (typeof value === "boolean") return <div className={`space-y-1.5 ${wide ? "sm:col-span-2" : ""}`}><div className="flex items-center justify-between gap-3"><Label htmlFor={name}>{displayLabel}</Label><Switch id={name} checked={value} onCheckedChange={(checked) => set(name,checked)} /></div>{name === "enablePrivateProcfs" && <p className="text-xs text-muted-foreground">Required by Chromium and toolchains that inspect /proc. Your cluster must support pod user namespaces and unmasked proc mounts.</p>}{name === "manageBreakGlass" && <p className="text-xs text-muted-foreground">Configure exceptional access requests for tools blocked by this policy.</p>}</div>;
  const multiline = ["instructions","description","commandPath","commandPathPrepend","commandPathAppend","extraReadOnlyPaths","extraWritablePaths","commandEnv","resourceRequests","resourceLimits"].includes(name) || name.endsWith("-message");
  const numeric = ["maxTurns", "subagentMaxTurns", "maxRuntimeMinutes", "maxRetries", "maxConcurrentSubagents", "maxConcurrentRuns", "perNamespaceMaxConcurrentRuns"].includes(name);
  const selectOptions: Record<string,string[]> = { permissionMode:["read-only","workspace-write","danger-full-access"], egressMode:["unrestricted","restricted","disabled"], defaultAction:["Allow","Deny"] };
  const control = selectOptions[name]
    ? <select id={name} className="h-9 w-full rounded-md border bg-background px-3 text-sm" value={value} disabled={disabled} onChange={(event) => set(name,event.target.value)}>{selectOptions[name].map((option) => <option key={option} value={option}>{option}</option>)}</select>
    : multiline
      ? <Textarea id={name} value={value} onChange={(event) => set(name,event.target.value)} rows={name === "instructions" ? 5 : 3} />
      : <Input id={name} type={numeric ? "number" : "text"} min={numeric ? 0 : undefined} value={value} disabled={disabled} onChange={(event) => set(name,event.target.value)} />;
  return <div className={`space-y-1.5 ${wide ? "sm:col-span-2" : ""}`}><Label htmlFor={name}>{displayLabel}</Label>{control}{["commandPath","commandPathPrepend","commandPathAppend","extraReadOnlyPaths"].includes(name) && <p className="text-xs text-muted-foreground">One absolute path per line.</p>}{name === "extraWritablePaths" && <p className="text-xs text-muted-foreground">One absolute scratch or cache path per line. Workspace, home, and system paths are rejected.</p>}{["commandEnv","resourceRequests","resourceLimits"].includes(name) && <p className="text-xs text-muted-foreground">JSON object with string values.</p>}{name === "allowedServers" && <p className="text-xs text-muted-foreground">Comma-separated server or server:tool|tool entries.</p>}{name === "breakGlassEnabled" && <p className="text-xs text-muted-foreground">Allows blocked capabilities to request explicit approval; it does not grant them automatically.</p>}{name === "model" && <p className="text-xs text-muted-foreground">Legacy compatibility value. Runtime role routing uses provider models only.</p>}{name === "providerModels" && <p className="text-xs text-muted-foreground">Comma-separated provider=model entries. Providers without an entry inherit the parent model.</p>}{name === "reasoningLevel" && <p className="text-xs text-muted-foreground">Optional: none, low, medium, high, xhigh, or max. Blank inherits the main agent.</p>}</div>;
}
