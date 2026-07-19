import { useState } from "react";
import { Link } from "react-router-dom";
import { Briefcase } from "lucide-react";
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { useLinearProjects } from "@/hooks/useWatchedList";
import { ReadyBadge } from "@/components/ReadyBadge";
import { formatAge, formatPollTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import { cn } from "@/lib/utils";
import { toneSoft } from "@/lib/status";
import { CreateLinearProjectDialog } from "@/components/CreateLinearProjectDialog";
import { Button } from "@/components/ui/button";

export function LinearProjectList() {
  const { projects, loading, error, refetch } = useLinearProjects();
  const [query, setQuery] = useState("");
  const now = useNow();

  const filtered = filterByQuery(projects, query, (p) => [
    p.name, p.namespace, p.projectId, p.provider, p.approvedLabel,
  ]);

  return (
    <ResourceListPage
      title="Projects"
      description="Linear projects connected to the agent platform."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search Linear projects…"
      actions={<CreateLinearProjectDialog onCreated={() => void refetch()} />}
      loading={loading}
      error={error}
      onRetry={refetch}
      empty={!filtered.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={<Briefcase className="size-6" />}
      emptyTitle={query ? `No matches for "${query}"` : "No projects found"}
      emptyDescription={query ? "Clear the search to see all Linear projects." : "Connect a Linear project to get started."}
      emptyAction={!query ? <CreateLinearProjectDialog onCreated={() => void refetch()} trigger={<Button variant="outline">Create project</Button>} /> : undefined}
    >
      <Table>
        <TableCaption className="sr-only">Linear projects</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead className="hidden md:table-cell">Project ID</TableHead>
            <TableHead className="hidden md:table-cell">Label</TableHead>
            <TableHead className="hidden md:table-cell">Provider</TableHead>
            <TableHead className="hidden md:table-cell">Auto-Create</TableHead>
            <TableHead className="hidden text-right md:table-cell">Processed</TableHead>
            <TableHead className="hidden md:table-cell">Last Poll</TableHead>
            <TableHead>Status</TableHead>
            <TableHead className="text-right">Age</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((p) => (
            <TableRow key={`${p.namespace}/${p.name}`}>
              <TableCell>
                <Link
                  to={`/linear/${p.namespace}/${p.name}`}
                  className="font-medium text-primary hover:underline"
                >
                  {p.name}
                </Link>
              </TableCell>
              <TableCell className="hidden font-mono text-sm text-muted-foreground md:table-cell">
                {p.projectId}
              </TableCell>
              <TableCell className="hidden text-sm text-muted-foreground md:table-cell">
                {p.approvedLabel || "ai-approved"}
              </TableCell>
              <TableCell className="hidden text-sm text-muted-foreground md:table-cell">
                {p.provider || "anthropic"}
              </TableCell>
              <TableCell className="hidden text-sm text-muted-foreground md:table-cell">
                <span className={cn("inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium", p.autoCreateTasks ? toneSoft.success : toneSoft.neutral)}>
                  {p.autoCreateTasks ? "Auto" : "Manual"}
                </span>
              </TableCell>
              <TableCell className="hidden text-right md:table-cell">{p.issuesProcessed}</TableCell>
              <TableCell className="hidden text-muted-foreground md:table-cell">
                {formatPollTime(p.lastPollTimeUnix, now)}
              </TableCell>
              <TableCell>
                <ReadyBadge status={p.conditionReady} />
              </TableCell>
              <TableCell className="text-right text-muted-foreground">
                {formatAge(p.createdAtUnix, now)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </ResourceListPage>
  );
}
