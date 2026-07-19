import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Clock3, Plus } from "lucide-react";
import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { ReadyBadge } from "@/components/ReadyBadge";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { Button } from "@/components/ui/button";
import { CronFormDialog } from "@/components/CronFormDialog";
import { useCrons } from "@/hooks/useWatchedList";
import { formatAge, formatScheduleTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";

export function CronList() {
  const { crons, loading, error, refetch } = useCrons();
  const [query, setQuery] = useState("");
  const now = useNow();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  const filtered = filterByQuery(crons, query, (cron) => [
    cron.name,
    cron.namespace,
    cron.schedule,
    cron.timeZone,
    cron.provider,
    cron.model,
    cron.repoUrl,
    cron.prompt,
  ]);

  return (
    <ResourceListPage
      title="Cron Triggers"
      description="Scheduled triggers that launch agent runs automatically."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search cron triggers…"
      loading={loading}
      error={error}
      onRetry={refetch}
      empty={!filtered.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={<Clock3 className="size-6" />}
      emptyTitle={query ? `No matches for "${query}"` : "No cron triggers found"}
      emptyDescription={query ? "Clear the search to see all cron triggers." : "Create a Cron resource to schedule recurring agent runs."}
      actions={
        <CronFormDialog
          defaultOpen={searchParams.get("new") === "1"}
          trigger={
            <Button size="sm">
              <Plus />
              New cron
            </Button>
          }
          onSaved={(cron) => {
            refetch();
            navigate(`/cron/${cron.namespace}/${cron.name}`);
          }}
        />
      }
    >
      <Table>
        <TableCaption className="sr-only">Cron triggers</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Schedule</TableHead>
            <TableHead>Time Zone</TableHead>
            <TableHead>Provider</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Last Run</TableHead>
            <TableHead>Next Run</TableHead>
            <TableHead className="text-right">Runs</TableHead>
            <TableHead className="text-right">Age</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((cron) => (
            <TableRow key={`${cron.namespace}/${cron.name}`}>
              <TableCell>
                <Link
                  to={`/cron/${cron.namespace}/${cron.name}`}
                  className="font-medium text-primary hover:underline"
                >
                  {cron.name}
                </Link>
              </TableCell>
              <TableCell className="font-mono text-sm text-muted-foreground">
                {cron.schedule}
              </TableCell>
              <TableCell className="text-sm text-muted-foreground">
                {cron.timeZone || "UTC"}
              </TableCell>
              <TableCell className="text-sm text-muted-foreground">{cron.provider || "openai"}</TableCell>
              <TableCell>
                {cron.suspend ? <Badge variant="secondary">Suspended</Badge> : <ReadyBadge status={cron.conditionReady} />}
              </TableCell>
              <TableCell className="text-muted-foreground">
                {formatScheduleTime(cron.lastScheduleTimeUnix, now)}
              </TableCell>
              <TableCell className="text-muted-foreground">
                {formatScheduleTime(cron.nextScheduleTimeUnix, now)}
              </TableCell>
              <TableCell className="text-right">{cron.runsCreated}</TableCell>
              <TableCell className="text-right text-muted-foreground">
                {formatAge(cron.createdAtUnix, now)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </ResourceListPage>
  );
}
