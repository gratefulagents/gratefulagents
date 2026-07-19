import type { UsageTask } from "@/rpc/platform/service_pb";

function renderValue(value: bigint | undefined, known?: boolean) {
  if (!known) return "unknown";
  return Number(value ?? 0).toLocaleString();
}

export function RunAttemptDetailsTable({ tasks }: { tasks: UsageTask[] }) {
  const attempts = tasks.flatMap((task) => task.attempts.map((attempt) => ({ ...attempt, taskId: task.taskId })));
  if (!attempts.length) {
    return <p className="text-sm text-muted-foreground">No attempts recorded.</p>;
  }
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-muted-foreground border-b">
          <th className="py-1 pr-2">Task</th>
          <th className="py-1 pr-2">Phase</th>
          <th className="py-1 pr-2">Step</th>
          <th className="py-1 pr-2">Model</th>
          <th className="py-1 pr-2">Provider</th>
          <th className="py-1 text-right">Total</th>
        </tr>
      </thead>
      <tbody>
        {attempts.map((attempt) => (
          <tr key={attempt.attemptId} className="border-b last:border-0">
            <td className="py-1 pr-2">{attempt.taskId}</td>
            <td className="py-1 pr-2">{attempt.phase || "—"}</td>
            <td className="py-1 pr-2">{attempt.step || "—"}</td>
            <td className="py-1 pr-2">{attempt.model || "—"}</td>
            <td className="py-1 pr-2">{attempt.provider || "—"}</td>
            <td className="py-1 text-right">{renderValue(attempt.usage?.totalTokens, attempt.usage?.tokensKnown)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
