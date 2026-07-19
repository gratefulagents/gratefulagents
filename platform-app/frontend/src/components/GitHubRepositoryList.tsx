import { useState } from "react";
import { Link } from "react-router-dom";
import { GitBranch as GithubIcon } from "lucide-react";
import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { AddGitHubRepositoryDialog } from "@/components/AddGitHubRepositoryDialog";
import { ResourceListPage } from "@/components/list-page";
import { useGitHubRepositories } from "@/hooks/useWatchedList";
import { formatAge, formatPollTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";

export function GitHubRepositoryList() {
  const { repositories, loading, error, refetch } = useGitHubRepositories();
  const [query, setQuery] = useState("");
  const now = useNow();

  const filtered = filterByQuery(repositories, query, (r) => [
    r.name, r.namespace, r.owner, r.repo, r.provider,
  ]);

  const isEmpty = filtered.length === 0;

  return (
    <ResourceListPage
      title="GitHub Repositories"
      description="Repositories the agent can open pull requests against."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search repositories…"
      loading={loading}
      error={error}
      onRetry={refetch}
      actions={<AddGitHubRepositoryDialog onCreated={refetch} />}
      empty={isEmpty}
      skeleton={<TableRowSkeleton rows={4} />}
      emptyIcon={<GithubIcon className="size-6" />}
      emptyTitle={query ? `No matches for "${query}"` : "No repositories connected"}
      emptyDescription={query ? "Clear the search to see all repositories." : "Create a GitHubRepository resource to poll issues from GitHub."}
      emptyAction={<AddGitHubRepositoryDialog onCreated={refetch} />}
    >
      <Table>
        <TableCaption className="sr-only">GitHub repositories</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Repository</TableHead>
            <TableHead className="hidden md:table-cell">Provider</TableHead>
            <TableHead className="hidden text-right md:table-cell">Processed</TableHead>
            <TableHead className="hidden md:table-cell">Last Poll</TableHead>
            <TableHead className="text-right">Age</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((r) => (
            <TableRow key={`${r.namespace}/${r.name}`}>
              <TableCell>
                <Link
                  to={`/github/${r.namespace}/${r.name}`}
                  className="font-medium text-primary hover:underline"
                >
                  {r.name}
                </Link>
              </TableCell>
              <TableCell className="font-mono text-sm text-muted-foreground">
                {r.owner}/{r.repo}
              </TableCell>
              <TableCell className="hidden text-sm text-muted-foreground md:table-cell">{r.provider || "openai"}</TableCell>
              <TableCell className="hidden text-right md:table-cell">{r.issuesProcessed}</TableCell>
              <TableCell className="hidden text-muted-foreground md:table-cell">
                {formatPollTime(r.lastPollTimeUnix, now)}
              </TableCell>
              <TableCell className="text-right text-muted-foreground">
                {formatAge(r.createdAtUnix, now)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </ResourceListPage>
  );
}
