import { useMemo } from "react";

import type { UsageTask } from "@/rpc/platform/service_pb";

function taskTokens(task: UsageTask): number | null {
  return task.usage?.tokensKnown ? Number(task.usage.totalTokens) : null;
}

export function RunUsageBreakdownTable({ title, tasks }: { title: string; tasks: UsageTask[] }) {
  const rows = useMemo(() => {
    const sorted = [...tasks].sort((a, b) => (taskTokens(b) ?? -1) - (taskTokens(a) ?? -1));
    const total = sorted.reduce((sum, task) => sum + (taskTokens(task) ?? 0), 0);
    return sorted.map((task) => {
      const tokens = taskTokens(task);
      return { task, tokens, pct: tokens !== null && total > 0 ? (tokens / total) * 100 : 0 };
    });
  }, [tasks]);

  if (!tasks.length) {
    return <p className="text-sm text-muted-foreground">No usage recorded.</p>;
  }
  return (
    <div className="space-y-2">
      <h4 className="text-sm font-medium">{title}</h4>
      <div className="overflow-x-auto">
        <table className="w-full text-sm tabular-nums">
          <thead>
            <tr className="border-b text-left text-muted-foreground">
              <th className="py-1 pr-2 font-medium">Task</th>
              <th className="py-1 pr-2 font-medium">Agent</th>
              <th className="py-1 pr-2 text-right font-medium">Attempts</th>
              <th className="py-1 text-right font-medium">Tokens</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ task, tokens, pct }) => (
              <tr key={task.taskId} className="border-b last:border-0">
                <td className="max-w-[160px] truncate py-1 pr-2" title={task.taskId}>
                  {task.taskId}
                </td>
                <td className="max-w-[160px] truncate py-1 pr-2" title={task.agentName || undefined}>
                  {task.agentName || "—"}
                </td>
                <td className="py-1 pr-2 text-right">{task.attempts.length}</td>
                <td className="py-1 text-right">
                  <span title={tokens === null ? "Token counts were not reported" : undefined}>
                    {tokens === null ? "—" : tokens.toLocaleString()}
                  </span>
                  <div className="mt-0.5 h-1 w-full overflow-hidden rounded-full bg-muted/60" aria-hidden>
                    <div
                      className="h-1 rounded-full bg-tone-running/60 transition-[width] duration-[var(--dur-base)]"
                      style={{ width: `${pct}%` }}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
