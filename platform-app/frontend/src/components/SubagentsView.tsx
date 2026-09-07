import { useMemo, useState } from "react";
import { GitBranch, List } from "lucide-react";

import { ActivityLogTable } from "@/components/FullActivityLog";
import { MarkdownViewer } from "@/components/MarkdownViewer";
import { SubagentGraphView } from "@/components/SubagentGraphView";
import { SubagentResultSection, subagentLiveLine } from "@/components/activity-log/SubagentCards";
import { cleanSubagentDescription, subagentTitleFromPrompt } from "@/lib/activityGrouping";
import { formatClock } from "@/lib/activityLogFormat";
import { subagentDetailEntries } from "@/lib/subagentDetailEntries";
import { nodeIDToTaskID } from "@/lib/subagentGraphLayout";
import { classifySubagentStatus, subagentStatusLabel } from "@/lib/subagentStatus";
import { cn } from "@/lib/utils";
import type { ActivityEntry, SubagentGraph, SubagentGraphNode } from "@/rpc/platform/service_pb";

const FILTERS = ["All", "Running", "Waiting", "Failed", "Completed"] as const;
type Filter = typeof FILTERS[number];
const BUTTON = "rounded-md px-2.5 py-1.5 text-xs focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2";

function taskTitle(node: SubagentGraphNode): string {
  return subagentTitleFromPrompt(cleanSubagentDescription(node.description))
    || cleanSubagentDescription(node.label)
    || "Unnamed task";
}

function taskState(node: SubagentGraphNode): { filter?: Filter; label: string } {
  const category = classifySubagentStatus(node.status);
  if ((category === "live" || category === "waiting") && node.durationMs > 0n) {
    return { label: "Finished · status unconfirmed" };
  }
  if (category === "live" || category === "waiting") {
    return category === "waiting" || node.waitingOn.length > 0
      ? { filter: "Waiting", label: "Waiting" }
      : { filter: "Running", label: "Running" };
  }
  if (category === "succeeded") return { filter: "Completed", label: "Completed" };
  const label = subagentStatusLabel(node.status);
  return { filter: category === "failed" ? "Failed" : undefined, label: label.charAt(0).toUpperCase() + label.slice(1) };
}

export function SubagentsView({ graph, entries, selectionRequest }: {
  graph?: SubagentGraph;
  entries: ActivityEntry[];
  selectionRequest?: { taskId: string };
}) {
  const tasks = useMemo(() => (graph?.nodes ?? [])
    .filter((node) => node.kind !== "root")
    .slice()
    .sort((a, b) => a.timestampUnix < b.timestampUnix ? -1 : a.timestampUnix > b.timestampUnix ? 1 : a.id.localeCompare(b.id))
    .map((node) => {
      const activity = subagentDetailEntries(node, entries);
      const latest = activity.at(-1);
      return {
        node,
        activity,
        latest,
        title: taskTitle(node),
        state: taskState(node),
        latestText: (latest ? subagentLiveLine([latest]) : "") || latest?.recentAction
          || latest?.message || latest?.step
          || (latest?.subagentResultText ? "Result reported" : "")
          || (latest?.tool ? `${latest.type.replaceAll("_", " ")}: ${latest.tool}` : "")
          || node.currentStep || (node.lastTool ? `Last tool: ${node.lastTool}` : "No activity available yet"),
      };
    }), [graph, entries]);
  const [filter, setFilter] = useState<Filter>("All");
  const [showGraph, setShowGraph] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [appliedSelection, setAppliedSelection] = useState<typeof selectionRequest>();
  const requestedTask = selectionRequest && selectionRequest !== appliedSelection
    ? tasks.find(({ node }) => node.taskId === selectionRequest.taskId
      || nodeIDToTaskID(node.id) === nodeIDToTaskID(selectionRequest.taskId))
    : undefined;
  if (requestedTask) {
    setAppliedSelection(selectionRequest);
    setShowGraph(false);
    setFilter("All");
  }
  const selected = requestedTask ?? tasks.find(({ node }) => node.id === selectedId) ?? tasks[0];
  const resolvedId = selected?.node.id ?? null;
  if (selectedId !== resolvedId) setSelectedId(resolvedId);

  const visibleTasks = tasks.filter((task) => filter === "All" || task.state.filter === filter);
  const node = selected?.node;
  const dependencyName = (id: string) => {
    const dependency = graph?.nodes.find((candidate) => candidate.id === id || candidate.taskId === id
      || nodeIDToTaskID(candidate.id) === nodeIDToTaskID(id));
    return dependency ? taskTitle(dependency) : `Unavailable task (${nodeIDToTaskID(id)})`;
  };
  const dependencies = node ? Array.from(new Set([
    ...node.dependsOn,
    ...(graph?.edges.filter((edge) => edge.kind === "depends-on" && edge.to === node.id).map((edge) => edge.from) ?? []),
  ].map(nodeIDToTaskID))) : [];
  const resultEntry = selected?.activity.slice().reverse().find((entry) => entry.type === "tool_result"
    && Boolean(node?.toolUseId) && entry.toolUseId === node?.toolUseId && Boolean(entry.output));
  const resultText = resultEntry?.output || selected?.activity.slice().reverse().find((entry) => Boolean(entry.subagentResultText))?.subagentResultText;
  const assignment = selected?.activity.find((entry) => entry.subagentPrompt)?.subagentPrompt || node?.description;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-border/50 px-4 py-3">
        <h3 className="text-sm font-medium">Subagents <span className="ml-1 text-muted-foreground">{tasks.length}</span></h3>
        {tasks.length > 0 && (
          <button type="button" aria-pressed={showGraph} onClick={() => setShowGraph(!showGraph)} className={cn(BUTTON, "inline-flex items-center gap-1.5 border border-border text-muted-foreground hover:text-foreground")}>
            {showGraph ? <List className="size-3.5" /> : <GitBranch className="size-3.5" />}
            {showGraph ? "View list" : "View graph"}
          </button>
        )}
      </div>
      {tasks.length === 0 ? (
        <div className="p-4 text-sm text-muted-foreground">
          <p className="font-medium text-foreground">No subagents observed</p>
          <p className="mt-1">Tasks appear when this run delegates work to subagents.</p>
        </div>
      ) : (
        <>
          <div hidden={!showGraph} className="min-h-0 flex-1">
            {showGraph && <SubagentGraphView graph={graph} entries={entries} />}
          </div>
          <div hidden={showGraph} className="min-h-0 flex-1 overflow-auto">
            <div role="group" aria-label="Filter subagents" className="flex flex-wrap gap-1 border-b border-border/50 px-3 py-2">
              {FILTERS.map((value) => (
                <button key={value} type="button" aria-pressed={filter === value} onClick={() => setFilter(value)} className={cn(BUTTON, filter === value ? "bg-muted text-foreground" : "text-muted-foreground hover:bg-muted/50")}>
                  {value} <span className="ml-1 tabular-nums text-muted-foreground">{value === "All" ? tasks.length : tasks.filter((task) => task.state.filter === value).length}</span>
                </button>
              ))}
            </div>
            <div className="@container">
              <div className="grid @min-[48rem]:grid-cols-[minmax(18rem,2fr)_minmax(0,3fr)]">
                <section aria-label="Subagent tasks" className="min-w-0 border-b border-border/50 @min-[48rem]:border-r @min-[48rem]:border-b-0">
                  {visibleTasks.length === 0 && <p className="p-4 text-sm text-muted-foreground">No {filter.toLowerCase()} subagents</p>}
                  <ul className="max-h-[45vh] overflow-auto @min-[48rem]:max-h-[65vh]">
                    {visibleTasks.map((task) => (
                      <li key={task.node.id} className="border-b border-border/40 last:border-b-0">
                        <button type="button" aria-pressed={selectedId === task.node.id} onClick={() => setSelectedId(task.node.id)} className={cn("w-full space-y-2 border-l-2 p-4 text-left focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-[-2px]", selectedId === task.node.id ? "border-l-primary bg-muted/50" : "border-l-transparent hover:bg-muted/25")}>
                          <span className="block break-words text-sm font-medium leading-relaxed">{task.title}</span>
                          <span className={cn("block text-xs font-medium", task.state.filter === "Failed" ? "text-tone-danger-fg" : task.state.filter === "Waiting" ? "text-tone-warning-fg" : "text-muted-foreground")}>{task.state.label}</span>
                          <span className="block text-xs leading-relaxed text-muted-foreground"><span className="font-medium">Latest: </span>{task.latestText}</span>
                          {task.state.filter === "Waiting" && task.node.waitingOn.length > 0 && <span className="block text-xs text-tone-warning-fg">Waiting on: {task.node.waitingOn.map(dependencyName).join(", ")}</span>}
                        </button>
                      </li>
                    ))}
                  </ul>
                </section>
                {selected && node && (
                  <section aria-label="Subagent detail" className="min-w-0 space-y-5 p-4">
                    <div>
                      <h4 className="break-words text-base font-medium">{selected.title}</h4>
                      <p className="mt-1 text-xs text-muted-foreground">{selected.state.label}</p>
                      {filter !== "All" && selected.state.filter !== filter && <p className="mt-2 text-xs text-muted-foreground">Selected task is outside this filter.</p>}
                    </div>
                    <section aria-label="Assignment" className="space-y-2">
                      <h5 className="text-sm font-medium">Assignment</h5>
                      <dl className="space-y-1 break-words text-xs text-muted-foreground">
                        {node.subtitle && <div><dt className="inline">Agent: </dt><dd className="inline">{node.kind === "inline-subagent" ? node.label : node.subtitle}</dd></div>}
                        {node.taskId && <div><dt className="inline">Task ID: </dt><dd className="inline font-mono">{node.taskId}</dd></div>}
                        {node.model && <div><dt className="inline">Model: </dt><dd className="inline">{node.model}</dd></div>}
                      </dl>
                      {assignment ? <MarkdownViewer content={assignment} /> : <p className="text-xs text-muted-foreground">No assignment text available.</p>}
                      {dependencies.length > 0 && <p className="text-xs text-muted-foreground">Depends on: {dependencies.map(dependencyName).join(", ")}</p>}
                      {selected.state.filter === "Waiting" && node.waitingOn.length > 0 && <p className="text-xs text-tone-warning-fg">Waiting on: {node.waitingOn.map(dependencyName).join(", ")}</p>}
                      {node.lastParentMessage && <p className="border-l-2 border-border pl-3 text-xs text-muted-foreground">Parent message: {node.lastParentMessage}</p>}
                      {node.lineageReason && <p className="text-xs text-muted-foreground">{node.lineageReason}</p>}
                    </section>
                    {resultText && <SubagentResultSection entry={resultEntry} fallbackText={resultText} isError={resultEntry?.isError ?? false} />}
                    <section aria-label="Activity" className="space-y-2">
                      <h5 className="text-sm font-medium">Activity</h5>
                      <p className="break-words text-xs text-muted-foreground">{selected.latest?.timestampUnix ? `${formatClock(selected.latest.timestampUnix)} · ` : ""}{selected.latestText}</p>
                      {node.stopReason && <p className="text-xs text-muted-foreground">Stop reason: {node.stopReason}</p>}
                      {selected.activity.length > 0
                        ? <ActivityLogTable entries={selected.activity} loading={false} error={null} />
                        : <p className="text-xs text-muted-foreground">No related activity loaded.</p>}
                    </section>
                  </section>
                )}
              </div>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
