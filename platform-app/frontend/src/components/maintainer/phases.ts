import type { MaintainerWorkItem } from "@/rpc/platform/service_pb";
import type { StatusTone } from "@/lib/status";

export type ColumnId =
  | "needs-you"
  | "triage"
  | "ready"
  | "in-flight"
  | "ready-to-merge"
  | "shipped";

export type Column = {
  id: ColumnId;
  label: string;
  tone: StatusTone;
  /** Collapsed by default; shows only a count badge. */
  collapsedByDefault?: boolean;
};

export const COLUMNS: Column[] = [
  { id: "needs-you", label: "Needs you", tone: "warning" },
  { id: "triage", label: "Triage", tone: "neutral" },
  { id: "ready", label: "Ready", tone: "info" },
  { id: "in-flight", label: "In flight", tone: "running" },
  { id: "ready-to-merge", label: "Ready to merge", tone: "purple" },
  { id: "shipped", label: "Shipped", tone: "success", collapsedByDefault: true },
];

export function itemColumn(item: MaintainerWorkItem): ColumnId {
  if (item.phase === "Delivered" || item.disposition === "NotActionable") return "shipped";
  if (item.phase === "AwaitingDecision") return "needs-you";
  if (item.phase === "ReadyToDispatch") return "ready";
  if (item.phase === "Dispatched" || item.phase === "Implementing") return "in-flight";
  if (item.phase === "ReadyToMerge") return "ready-to-merge";
  // PendingTriage, Triaged (with or without graph_configured)
  return "triage";
}

/** Phase label + tone for a work item. */
export function itemPhaseTone(item: MaintainerWorkItem): { label: string; tone: StatusTone } {
  if (item.disposition === "NotActionable") return { label: "Not actionable", tone: "neutral" };
  switch (item.phase) {
    case "AwaitingDecision":
      return { label: "Needs decision", tone: "warning" };
    case "ReadyToDispatch":
      return { label: "Ready to dispatch", tone: "info" };
    case "Dispatched":
      return { label: "Dispatched", tone: "running" };
    case "Implementing":
      return { label: "Implementing", tone: "running" };
    case "ReadyToMerge":
      return { label: "Ready to merge", tone: "purple" };
    case "Delivered":
      return { label: "Delivered", tone: "success" };
    case "Triaged":
      return { label: "Triaged", tone: "neutral" };
    case "PendingTriage":
    default:
      return { label: "Pending triage", tone: "neutral" };
  }
}
