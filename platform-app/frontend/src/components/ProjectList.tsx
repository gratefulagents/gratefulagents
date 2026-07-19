import { useState } from "react";
import { Link } from "react-router-dom";
import { FolderKanban } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { ProjectCredentialBadges } from "@/components/projectCredentials";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { useProjects } from "@/hooks/useWatchedList";
import { formatAge, formatCount } from "@/lib/format";

export function ProjectList() {
  const { projects, loading, error, refetch } = useProjects();
  const [query, setQuery] = useState("");

  const filtered = filterByQuery(projects, query, (p) => [
    p.name, p.displayName, p.namespace, p.provider,
  ]);

  return (
    <ResourceListPage
      title="Projects"
      description="Coding agent projects and their run history."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search projects…"
      actions={<CreateProjectDialog />}
      loading={loading}
      error={error}
      onRetry={refetch}
      empty={!filtered.length}
      skeleton={<TableRowSkeleton rows={6} />}
      emptyIcon={<FolderKanban className="size-6" />}
      emptyTitle={query ? `No matches for "${query}"` : "No projects yet"}
      emptyDescription={
        query
          ? "Clear the search to see all projects."
          : "Create a project to start orchestrating agent runs."
      }
    >
      <Table>
        <TableCaption className="sr-only">Dashboard projects</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Owner</TableHead>
            <TableHead className="hidden xl:table-cell">Display Name</TableHead>
            <TableHead className="hidden md:table-cell">Provider</TableHead>
            <TableHead className="hidden md:table-cell">Credentials</TableHead>
            <TableHead className="text-right">Runs</TableHead>
            <TableHead className="hidden text-right md:table-cell">Cost</TableHead>
            <TableHead className="hidden text-right md:table-cell">Tokens</TableHead>
            <TableHead className="text-right">Age</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((p) => (
            <TableRow key={`${p.namespace}/${p.name}`}>
              <TableCell>
                <div className="flex flex-wrap items-center gap-2">
                  <Link
                    to={`/projects/${p.namespace}/${p.name}`}
                    className="font-medium text-primary hover:underline"
                  >
                    {p.name}
                  </Link>
                  {p.kubernetesAdmin && (
                    <Badge variant="secondary" className="text-xs">Kubernetes admin</Badge>
                  )}
                </div>
              </TableCell>
              <TableCell><OwnerAvatar owner={p.owner} /></TableCell>
              <TableCell className="hidden text-muted-foreground xl:table-cell">{p.displayName}</TableCell>
              <TableCell className="hidden text-sm text-muted-foreground md:table-cell">{p.provider || "openai"}</TableCell>
              <TableCell className="hidden md:table-cell">
                <ProjectCredentialBadges project={p} />
              </TableCell>
              <TableCell className="text-right">{p.metrics?.totalRuns ?? 0}</TableCell>
              <TableCell className="hidden text-right font-mono md:table-cell">
                {(p.metrics?.totalCostUsd ?? 0) > 0
                  ? `$${p.metrics!.totalCostUsd.toFixed(2)}`
                  : "-"}
              </TableCell>
              <TableCell className="hidden text-right font-mono text-muted-foreground md:table-cell">
                {formatCount(
                  Number(p.metrics?.totalInputTokens ?? 0n) +
                  Number(p.metrics?.totalOutputTokens ?? 0n)
                )}
              </TableCell>
              <TableCell className="text-right text-muted-foreground">
                {formatAge(p.createdAtUnix)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </ResourceListPage>
  );
}
