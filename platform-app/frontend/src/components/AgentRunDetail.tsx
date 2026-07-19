import { useParams } from "react-router-dom";
import { useAgentRun } from "@/hooks/useAgentRun";
import { RunSessionView } from "@/components/RunSessionView";
import { ListRowSkeleton, ListState } from "@/components/ui/list-state";

export function AgentRunDetail() {
  const { namespace = "", name = "" } = useParams();
  const { run, loading, error, starting } = useAgentRun(namespace, name);

  return (
    <ListState
      loading={loading}
      error={error}
      empty={!run}
      skeleton={
        <div className="space-y-3">
          {starting && (
            <p className="text-[12.5px] text-muted-foreground">
              Run is starting — waiting for it to appear…
            </p>
          )}
          <ListRowSkeleton rows={6} />
        </div>
      }
      emptyTitle="Run not found"
      emptyDescription="This run may have been removed or you may not have access."
    >
      <RunSessionView key={`${namespace}/${name}`} namespace={namespace} name={name} />
    </ListState>
  );
}
