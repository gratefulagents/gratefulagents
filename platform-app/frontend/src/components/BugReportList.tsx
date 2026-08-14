/* eslint-disable react-hooks/set-state-in-effect */
import { Fragment, useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Code } from "@connectrpc/connect";
import { Bug, ChevronDown, ChevronRight, Filter, SquareArrowOutUpRight } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { connectCodeOf } from "@/lib/rpc-errors";
import { toneSoft, type StatusTone } from "@/lib/status";
import { formatAge } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import type { BugReport } from "@/rpc/platform/service_pb";

const BUG_REPORT_STATUSES = ["open", "acknowledged", "resolved", "dismissed"] as const;
const BUG_REPORT_CATEGORIES = ["bug", "complaint", "feature"] as const;

const POSTGRES_REQUIRED_MESSAGE =
  "Bug reports require the Postgres state store. Configure Postgres on the backend to enable agent-filed bug report triage.";

const CATEGORY_TONES: Record<string, StatusTone> = {
  bug: "danger",
  complaint: "warning",
  feature: "info",
};

const STATUS_TONES: Record<string, StatusTone> = {
  open: "warning",
  acknowledged: "info",
  resolved: "success",
  dismissed: "neutral",
};

function lastSeenUnix(report: BugReport): bigint {
  const ts: Timestamp | undefined = report.lastSeenAt ?? report.firstSeenAt;
  if (!ts) return 0n;
  return BigInt(Math.floor(timestampDate(ts).getTime() / 1000));
}

function FilterSelect({
  label,
  value,
  onChange,
  children,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  children: ReactNode;
}) {
  return (
    <label className="flex min-w-[130px] flex-1 items-center gap-1.5 text-xs text-muted-foreground sm:min-w-0 sm:flex-none">
      <span className="sr-only">{label}</span>
      <select
        aria-label={label}
        value={value}
        onChange={(event) => onChange(event.currentTarget.value)}
        className={`h-7 w-full rounded-lg border px-2 text-[12px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 sm:w-auto ${
          value === "all"
            ? "border-input bg-background dark:bg-input/30"
            : "border-primary/40 bg-primary/5"
        }`}
      >
        {children}
      </select>
    </label>
  );
}

export function BugReportList() {
  const [reports, setReports] = useState<BugReport[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState("");
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [categoryFilter, setCategoryFilter] = useState("all");
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const now = useNow();

  const fetchReports = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const resp = await client.listBugReports({ namespace: "" });
      setReports(resp.reports);
    } catch (e: unknown) {
      if (connectCodeOf(e) === Code.FailedPrecondition) {
        setError(POSTGRES_REQUIRED_MESSAGE);
      } else {
        setError(e instanceof Error ? e.message : "Failed to load bug reports");
      }
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchReports();
  }, [fetchReports]);

  const updateStatus = useCallback(async (report: BugReport, status: string) => {
    setActionError("");
    try {
      const updated = await client.updateBugReportStatus({
        namespace: report.namespace,
        id: report.id,
        status,
        note: "",
      });
      setReports((prev) => prev.map((r) => (r.id === updated.id ? updated : r)));
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to update report status");
    }
  }, []);

  const filtered = filterByQuery(reports, query, (report) => [
    report.title,
    report.toolName,
    report.runName,
    report.category,
    report.status,
  ]).filter(
    (report) =>
      (statusFilter === "all" || report.status === statusFilter) &&
      (categoryFilter === "all" || report.category === categoryFilter),
  );

  const filtersActive = statusFilter !== "all" || categoryFilter !== "all";

  return (
    <ResourceListPage
      title="Bug Reports"
      description="Agent-filed platform bug reports, complaints, and feature requests."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search bug reports…"
      loading={loading}
      error={error}
      onRetry={() => void fetchReports()}
      empty={!filtered.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={<Bug className="size-6" />}
      emptyTitle={
        query || filtersActive ? "No bug reports match the current filters" : "No bug reports"
      }
      emptyDescription={
        query || filtersActive
          ? "Clear the search or filters to see all bug reports."
          : "Reports filed by agents about the platform will appear here."
      }
      toolbar={
        <div
          className="flex flex-wrap items-center gap-2 rounded-lg border border-border/70 bg-muted/20 px-2.5 py-2"
          aria-label="Bug report filters"
          role="group"
        >
          <span className="inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
            <Filter className="size-3.5" aria-hidden />
            Filters
          </span>
          <FilterSelect label="Status" value={statusFilter} onChange={setStatusFilter}>
            <option value="all">All statuses</option>
            {BUG_REPORT_STATUSES.map((s) => (
              <option key={s} value={s}>{s}</option>
            ))}
          </FilterSelect>
          <FilterSelect label="Category" value={categoryFilter} onChange={setCategoryFilter}>
            <option value="all">All categories</option>
            {BUG_REPORT_CATEGORIES.map((c) => (
              <option key={c} value={c}>{c}</option>
            ))}
          </FilterSelect>
          {actionError && (
            <span className="basis-full text-[11px] text-destructive sm:ml-auto sm:basis-auto" role="alert">
              {actionError}
            </span>
          )}
        </div>
      }
    >
      <Table>
        <TableCaption className="sr-only">Bug reports</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead className="w-8" />
            <TableHead>Title</TableHead>
            <TableHead>Category</TableHead>
            <TableHead>Tool</TableHead>
            <TableHead>Occurrences</TableHead>
            <TableHead>Run</TableHead>
            <TableHead>Status</TableHead>
            <TableHead className="text-right">Last Seen</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((report) => {
            const expanded = expandedId === report.id;
            return (
              <Fragment key={report.id}>
                <TableRow>
                  <TableCell className="py-1">
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Toggle details for ${report.title}`}
                      aria-expanded={expanded}
                      onClick={() => setExpandedId(expanded ? null : report.id)}
                    >
                      {expanded ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
                    </Button>
                  </TableCell>
                  <TableCell className="max-w-[320px] truncate font-medium">{report.title}</TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={cn(
                        "capitalize border-transparent",
                        toneSoft[CATEGORY_TONES[report.category] ?? "neutral"],
                      )}
                    >
                      {report.category || "unknown"}
                    </Badge>
                  </TableCell>
                  <TableCell className="font-mono text-sm text-muted-foreground">
                    {report.toolName || "—"}
                  </TableCell>
                  <TableCell className="text-muted-foreground">{report.occurrences}</TableCell>
                  <TableCell>
                    {report.runName ? (
                      <Link
                        to={`/runs/${report.namespace}/${report.runName}`}
                        className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
                        aria-label={`View agent run ${report.runName}`}
                      >
                        <SquareArrowOutUpRight className="size-3" aria-hidden />
                        {report.runName}
                      </Link>
                    ) : (
                      <span className="text-sm text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <span className="inline-flex items-center gap-2">
                      <Badge
                        variant="outline"
                        className={cn(
                          "capitalize border-transparent",
                          toneSoft[STATUS_TONES[report.status] ?? "neutral"],
                        )}
                      >
                        {report.status || "unknown"}
                      </Badge>
                      <select
                        aria-label={`Set status for ${report.title}`}
                        value={report.status}
                        onChange={(event) => void updateStatus(report, event.currentTarget.value)}
                        className="h-7 rounded-lg border border-input bg-background px-2 text-[12px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 dark:bg-input/30"
                      >
                        {BUG_REPORT_STATUSES.map((s) => (
                          <option key={s} value={s}>{s}</option>
                        ))}
                      </select>
                    </span>
                  </TableCell>
                  <TableCell className="text-right text-muted-foreground">
                    {formatAge(lastSeenUnix(report), now)}
                  </TableCell>
                </TableRow>
                {expanded && (
                  <TableRow className="bg-muted/20 hover:bg-muted/20">
                    <TableCell colSpan={8} className="space-y-2 py-3">
                      <p className="whitespace-pre-wrap text-sm">{report.body || "No details provided."}</p>
                      {(report.statusNote || report.statusActor) && (
                        <p className="text-xs text-muted-foreground">
                          {report.statusNote && <>Note: {report.statusNote} </>}
                          {report.statusActor && <>— {report.statusActor}</>}
                        </p>
                      )}
                      {report.firstSeenAt && (
                        <p className="text-xs text-muted-foreground">
                          First seen {timestampDate(report.firstSeenAt).toLocaleString()}
                        </p>
                      )}
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            );
          })}
        </TableBody>
      </Table>
    </ResourceListPage>
  );
}
