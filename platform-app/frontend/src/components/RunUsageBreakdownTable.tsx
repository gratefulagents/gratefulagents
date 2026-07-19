import type { UsageTask } from "@/rpc/platform/service_pb";

function renderTokens(task: UsageTask) {
  return task.usage?.tokensKnown ? Number(task.usage.totalTokens).toLocaleString() : "unknown";
}

export function RunUsageBreakdownTable({ title, tasks }: { title: string; tasks: UsageTask[] }) {
  if (!tasks.length) {
    return <p className="text-sm text-muted-foreground">No usage recorded.</p>;
  }
  return (
    <div className="space-y-2">
      <h4 className="text-sm font-medium">{title}</h4>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-muted-foreground border-b">
              <th className="py-1 pr-2">Task</th>
              <th className="py-1 pr-2">Agent</th>
              <th className="py-1 pr-2">Attempts</th>
              <th className="py-1 text-right">Tokens</th>
            </tr>
          </thead>
          <tbody>
            {tasks.map((task) => (
              <tr key={task.taskId} className="border-b last:border-0">
                <td className="max-w-[160px] truncate py-1 pr-2" title={task.taskId}>
                  {task.taskId}
                </td>
                <td className="max-w-[160px] truncate py-1 pr-2" title={task.agentName || undefined}>
                  {task.agentName || "—"}
                </td>
                <td className="py-1 pr-2">{task.attempts.length}</td>
                <td className="py-1 text-right">{renderTokens(task)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
