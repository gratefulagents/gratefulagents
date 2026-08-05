/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { clone, create } from "@bufbuild/protobuf";
import { Pencil, Plus, ShieldCheck, Trash2 } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { ReadyBadge } from "@/components/ReadyBadge";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { SeverityCountBadges } from "@/components/SecurityScanList";
import {
  scanConfigUsesSavedCredentials,
  SecurityScanFormDialog,
} from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";
import { formatScheduleTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import {
  SecurityScanConfigSpecSchema,
  UpdateSecurityScanRequestSchema,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

export function SecurityScanConfigList() {
  const [configs, setConfigs] = useState<SecurityScanConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [pendingDelete, setPendingDelete] = useState<SecurityScanConfig | null>(null);
  const now = useNow();

  const fetchConfigs = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const resp = await client.listSecurityScanConfigs({ namespace: "" });
      setConfigs(resp.configs);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load security scan configurations");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchConfigs();
  }, [fetchConfigs]);

  async function toggleSuspend(config: SecurityScanConfig) {
    setActionError(null);
    const spec = config.spec
      ? clone(SecurityScanConfigSpecSchema, config.spec)
      : create(SecurityScanConfigSpecSchema, {});
    spec.suspend = !spec.suspend;
    try {
      await client.updateSecurityScan(
        create(UpdateSecurityScanRequestSchema, {
          namespace: config.namespace,
          name: config.name,
          spec,
          useSavedCredentials: scanConfigUsesSavedCredentials(config),
        }),
      );
      await fetchConfigs();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to update security scan");
    }
  }

  async function handleDelete(config: SecurityScanConfig) {
    setActionError(null);
    try {
      await client.deleteSecurityScan({ namespace: config.namespace, name: config.name });
      await fetchConfigs();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to delete security scan");
    }
  }

  const filtered = filterByQuery(configs, query, (config) => [
    config.name,
    config.namespace,
    config.spec?.repoUrl ?? "",
    config.spec?.schedule ?? "",
    config.phase,
  ]);

  return (
    <ResourceListPage
      title="Scan Configurations"
      description="Configured security scans that analyze repositories, once or on a schedule."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search scan configurations…"
      loading={loading}
      error={error}
      onRetry={fetchConfigs}
      empty={!filtered.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={<ShieldCheck className="size-6" />}
      emptyTitle={query ? `No matches for "${query}"` : "No scan configurations found"}
      emptyDescription={
        query
          ? "Clear the search to see all scan configurations."
          : "Create a security scan to analyze a repository for vulnerabilities."
      }
      actions={
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/security/runs" />}>
            Scan results
          </Button>
          <SecurityScanFormDialog
            trigger={
              <Button size="sm">
                <Plus />
                New scan
              </Button>
            }
            onSaved={() => void fetchConfigs()}
          />
        </div>
      }
    >
      {actionError && (
        <p role="alert" className="mb-3 text-sm text-destructive">
          {actionError}
        </p>
      )}
      <Table>
        <TableCaption className="sr-only">Security scan configurations</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Repository</TableHead>
            <TableHead>Schedule</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Findings</TableHead>
            <TableHead>Last Scan</TableHead>
            <TableHead className="text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((config) => (
            <TableRow key={`${config.namespace}/${config.name}`}>
              <TableCell className="font-medium">{config.name}</TableCell>
              <TableCell className="font-mono text-sm text-muted-foreground">
                {config.spec?.repoUrl || "—"}
              </TableCell>
              <TableCell className="font-mono text-sm text-muted-foreground">
                {config.spec?.schedule || "once"}
              </TableCell>
              <TableCell>
                {config.spec?.suspend ? (
                  <Badge variant="secondary">Suspended</Badge>
                ) : (
                  <ReadyBadge status={config.conditionReady} />
                )}
              </TableCell>
              <TableCell>
                <SeverityCountBadges counts={config.findingCounts} />
              </TableCell>
              <TableCell className="text-muted-foreground">
                {formatScheduleTime(config.lastScanTimeUnix, now)}
              </TableCell>
              <TableCell className="text-right">
                <div className="inline-flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => void toggleSuspend(config)}
                  >
                    {config.spec?.suspend ? "Resume" : "Suspend"}
                  </Button>
                  <SecurityScanFormDialog
                    config={config}
                    trigger={
                      <Button variant="ghost" size="sm" aria-label={`Edit ${config.name}`}>
                        <Pencil />
                      </Button>
                    }
                    onSaved={() => void fetchConfigs()}
                  />
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={`Delete ${config.name}`}
                    onClick={() => setPendingDelete(config)}
                  >
                    <Trash2 />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
        title={`Delete ${pendingDelete?.name ?? "scan"}?`}
        description="The scan configuration is removed; recorded findings stay available."
        confirmLabel="Delete"
        destructive
        onConfirm={async () => {
          if (pendingDelete) await handleDelete(pendingDelete);
          setPendingDelete(null);
        }}
      />
    </ResourceListPage>
  );
}
