import {
  memo,
  useCallback,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";
import type { GetAgentTraceResponse } from "@/rpc/platform/service_pb";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { aggregateTraceUsage } from "@/lib/traceUsage";
import {
  assembleRows,
  barGeometry,
  baseKind,
  buildWaterfall,
  computeTicks,
  fmtDurationUs,
  fmtOffsetUs,
  fmtTokensCompact,
  fmtUsd,
  KIND_ORDER,
  spanCostUsd,
  spanDisplayName,
  spanModel,
  spanTokens,
  type BaseKind,
  type RowFilter,
  type TurnGroup,
  type ViewWindow,
  type Waterfall,
  type WaterfallNode,
  type WaterfallRow,
} from "@/lib/traceWaterfall";
import { cn } from "@/lib/utils";
import {
  AlertTriangle,
  Bot,
  Brain,
  ChevronDown,
  ChevronRight,
  ChevronsDownUp,
  ChevronsUpDown,
  CircleDot,
  Clock,
  Coins,
  Copy,
  Cpu,
  GitBranch,
  Hash,
  Layers,
  RotateCcw,
  Search,
  ShieldAlert,
  Terminal,
  X,
  Zap,
} from "lucide-react";

// ---------------------------------------------------------------------------
// Kind presentation
// ---------------------------------------------------------------------------

type KindMeta = {
  label: string;
  icon: typeof Zap;
  text: string;
  bar: string;
  dot: string;
};

const KIND_META: Record<BaseKind, KindMeta> = {
  llm:        { label: "LLM",        icon: Brain,        text: "text-violet-400",  bar: "bg-violet-500",  dot: "bg-violet-400" },
  tool:       { label: "Tool",       icon: Terminal,     text: "text-emerald-400", bar: "bg-emerald-500", dot: "bg-emerald-400" },
  subagent:   { label: "Subagent",   icon: Bot,          text: "text-cyan-400",    bar: "bg-cyan-500",    dot: "bg-cyan-400" },
  agent:      { label: "Agent",      icon: Bot,          text: "text-indigo-400",  bar: "bg-indigo-500",  dot: "bg-indigo-400" },
  session:    { label: "Session",    icon: Cpu,          text: "text-sky-400",     bar: "bg-sky-500",     dot: "bg-sky-400" },
  handoff:    { label: "Handoff",    icon: GitBranch,    text: "text-teal-400",    bar: "bg-teal-500",    dot: "bg-teal-400" },
  guardrail:  { label: "Guardrail",  icon: ShieldAlert,  text: "text-orange-400",  bar: "bg-orange-500",  dot: "bg-orange-400" },
  compaction: { label: "Compaction", icon: Layers,       text: "text-yellow-500",  bar: "bg-yellow-500",  dot: "bg-yellow-500" },
  retry:      { label: "Retry",      icon: RotateCcw,    text: "text-rose-400",    bar: "bg-rose-500",    dot: "bg-rose-400" },
  phase:      { label: "Phase",      icon: Layers,       text: "text-amber-400",   bar: "bg-amber-400",   dot: "bg-amber-400" },
  other:      { label: "Other",      icon: Zap,          text: "text-muted-foreground", bar: "bg-muted-foreground/70", dot: "bg-muted-foreground" },
};

// Shared row grid: name | timeline | duration.
const ROW_GRID =
  "grid grid-cols-[minmax(0,1fr)_64px] sm:grid-cols-[minmax(230px,330px)_minmax(0,1fr)_64px]";

const GRIDLINE_BG =
  "linear-gradient(to right, rgb(148 163 184 / 0.14) 1px, transparent 1px)";

const RENDER_CAP = 1500;

// ---------------------------------------------------------------------------
// Shared hover tooltip (single instance — cheap for large traces)
// ---------------------------------------------------------------------------

type HoverTip = { x: number; y: number; node: WaterfallNode } | null;

function HoverCard({ tip, minStartUs }: { tip: HoverTip; minStartUs: bigint }) {
  if (!tip) return null;
  const { node } = tip;
  const span = node.span;
  const meta = KIND_META[baseKind(span.kind)];
  const offsetUs = Number(span.startTimeUnixUs - minStartUs);
  const tokens = spanTokens(span);
  const cost = spanCostUsd(span);
  const errText = span.isError
    ? span.tags.find((t) => ["gen.error", "tool.output"].includes(t.key))?.value
    : undefined;
  return (
    <div
      className="pointer-events-none fixed z-50 max-w-sm rounded-md border border-border bg-popover px-3 py-2 text-popover-foreground shadow-md"
      style={{ left: Math.min(tip.x + 12, window.innerWidth - 320), top: tip.y + 14 }}
    >
      <div className="flex items-center gap-1.5 text-xs font-medium">
        <span className={cn("size-2 rounded-full", span.isError ? "bg-destructive" : meta.dot)} />
        <span className="truncate">{spanDisplayName(span)}</span>
        <span className="text-muted-foreground">· {meta.label}</span>
      </div>
      <div className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono text-[11px] text-muted-foreground tabular-nums">
        <span>start</span>
        <span>+{fmtOffsetUs(offsetUs)}</span>
        <span>duration</span>
        <span className="text-foreground">{fmtDurationUs(span.durationUs)}</span>
        {tokens && (
          <>
            <span>tokens</span>
            <span>
              {fmtTokensCompact(tokens.input)} in / {fmtTokensCompact(tokens.output)} out
            </span>
          </>
        )}
        {cost !== undefined && (
          <>
            <span>cost</span>
            <span>{fmtUsd(cost)}</span>
          </>
        )}
      </div>
      {span.isError && (
        <p className="mt-1 line-clamp-3 break-all text-[11px] text-destructive">
          {errText || "error"}
        </p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Summary header
// ---------------------------------------------------------------------------

function StatItem({
  icon: Icon,
  className,
  children,
}: {
  icon: typeof Zap;
  className?: string;
  children: ReactNode;
}) {
  return (
    <span className={cn("inline-flex items-center gap-1.5", className)}>
      <Icon className="size-3" />
      <span className="font-mono text-[11px] tabular-nums">{children}</span>
    </span>
  );
}

function SummaryHeader({ wf, trace }: { wf: Waterfall; trace: GetAgentTraceResponse }) {
  const usage = useMemo(() => aggregateTraceUsage(trace.spans), [trace.spans]);
  const stats = useMemo(() => {
    let llm = 0;
    let llmUs = 0;
    let tools = 0;
    let toolUs = 0;
    let subagents = 0;
    let errors = 0;
    for (const n of wf.nodes) {
      const k = baseKind(n.span.kind);
      if (k === "llm") {
        llm++;
        llmUs += Number(n.span.durationUs);
      } else if (k === "tool") {
        tools++;
        toolUs += Number(n.span.durationUs);
      } else if (k === "subagent") subagents++;
      if (n.span.isError) errors++;
    }
    return { llm, llmUs, tools, toolUs, subagents, errors };
  }, [wf.nodes]);
  const totalUs = Number(wf.maxEndUs - wf.minStartUs);

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 border-b border-border/50 px-3 py-2 text-xs">
      <StatItem icon={Clock} className="text-foreground">
        {fmtDurationUs(totalUs)}
      </StatItem>
      <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
        {wf.nodes.length.toLocaleString()} spans
      </span>
      {stats.llm > 0 && (
        <StatItem icon={Brain} className="text-violet-400">
          {stats.llm} LLM · {fmtDurationUs(stats.llmUs)}
        </StatItem>
      )}
      {stats.tools > 0 && (
        <StatItem icon={Terminal} className="text-emerald-400">
          {stats.tools} tools · {fmtDurationUs(stats.toolUs)}
        </StatItem>
      )}
      {stats.subagents > 0 && (
        <StatItem icon={Bot} className="text-cyan-400">
          {stats.subagents} subagents
        </StatItem>
      )}
      {usage.hasUsage && (
        <StatItem icon={Hash} className="text-muted-foreground">
          {fmtTokensCompact(usage.inputTokens)} in / {fmtTokensCompact(usage.outputTokens)} out
        </StatItem>
      )}
      {usage.hasCost && (
        <StatItem icon={Coins} className="text-amber-400">
          {fmtUsd(usage.costUsd)}
        </StatItem>
      )}
      {stats.errors > 0 && (
        <StatItem icon={AlertTriangle} className="text-destructive">
          {stats.errors} errors
        </StatItem>
      )}
      <span className="ml-auto inline-flex items-center gap-3">
        {trace.serviceName && (
          <span className="hidden font-mono text-[10px] text-muted-foreground md:inline">
            {trace.serviceName}
          </span>
        )}
        {!trace.isComplete && (
          <span className="inline-flex items-center gap-1 text-primary">
            <CircleDot className="size-2.5 animate-pulse" />
            <span className="text-[10px] font-medium">LIVE</span>
          </span>
        )}
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Detail panel
// ---------------------------------------------------------------------------

const HIDDEN_TAGS = new Set(["internal.span.format", "span.kind"]);

function DetailPanel({
  node,
  minStartUs,
  onClose,
}: {
  node: WaterfallNode;
  minStartUs: bigint;
  onClose: () => void;
}) {
  const span = node.span;
  const meta = KIND_META[baseKind(span.kind)];
  const Icon = meta.icon;
  const tags = span.tags
    .filter((t) => !HIDDEN_TAGS.has(t.key) && t.value !== "")
    .sort((a, b) => a.key.localeCompare(b.key));
  const offsetUs = Number(span.startTimeUnixUs - minStartUs);
  const [copied, setCopied] = useState(false);

  const copyTags = () => {
    const payload = {
      name: span.operationName,
      kind: span.kind,
      spanId: span.spanId,
      startOffset: fmtOffsetUs(offsetUs),
      duration: fmtDurationUs(span.durationUs),
      tags: Object.fromEntries(span.tags.map((t) => [t.key, t.value])),
    };
    void navigator.clipboard?.writeText(JSON.stringify(payload, null, 2)).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    });
  };

  return (
    <div className="flex max-h-72 shrink-0 flex-col border-t border-border bg-muted/20">
      <div className="flex items-center gap-2 border-b border-border/50 px-3 py-1.5">
        <Icon className={cn("size-3.5 shrink-0", span.isError ? "text-destructive" : meta.text)} />
        <span className="truncate text-xs font-medium text-foreground">{span.operationName}</span>
        <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{meta.label}</span>
        {span.isError && <AlertTriangle className="size-3 shrink-0 text-destructive" />}
        <span className="ml-auto flex shrink-0 items-center gap-2 font-mono text-[10px] text-muted-foreground tabular-nums">
          <span>+{fmtOffsetUs(offsetUs)}</span>
          <span className="text-foreground">{fmtDurationUs(span.durationUs)}</span>
        </span>
        <button
          type="button"
          onClick={copyTags}
          className="inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <Copy className="size-3" />
          {copied ? "Copied" : "Copy"}
        </button>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close details"
          className="shrink-0 rounded p-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      </div>
      <div className="min-h-0 overflow-y-auto px-3 py-2">
        {tags.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">No attributes.</p>
        ) : (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
            {tags.map((t) => (
              <div key={t.key} className="contents">
                <dt className="font-mono text-[11px] whitespace-nowrap text-muted-foreground">{t.key}</dt>
                <dd className="font-mono text-[11px] break-all whitespace-pre-wrap text-foreground">
                  {t.value}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

const GroupRow = memo(function GroupRow({
  group,
  collapsed,
  onToggle,
  minStartUs,
  view,
  gridStyle,
}: {
  group: TurnGroup;
  collapsed: boolean;
  onToggle: (key: string) => void;
  minStartUs: bigint;
  view: ViewWindow;
  gridStyle: CSSProperties;
}) {
  const range = view.endUs - view.startUs;
  const start = Number(group.startUs - minStartUs);
  const end = Number(group.endUs - minStartUs);
  const leftPct = Math.max(((start - view.startUs) / range) * 100, 0);
  const rightPct = Math.min(((end - view.startUs) / range) * 100, 100);
  const visible = rightPct > 0 && leftPct < 100;
  return (
    <div
      className={cn(ROW_GRID, "group cursor-pointer border-t border-border/40 bg-muted/30 hover:bg-muted/50")}
      onClick={() => onToggle(group.key)}
      role="button"
      aria-expanded={!collapsed}
    >
      <div className="flex min-w-0 items-center gap-1.5 py-1 pr-2 pl-1.5">
        {collapsed ? (
          <ChevronRight className="size-3 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
        )}
        <span className="text-[11px] font-semibold tracking-wide text-foreground uppercase">
          {group.label}
        </span>
        <span className="truncate font-mono text-[10px] text-muted-foreground tabular-nums">
          {group.genCount > 0 && `${group.genCount} llm`}
          {group.toolCount > 0 && ` · ${group.toolCount} tools`}
          {collapsed && group.spanCount > 0 && ` · ${group.spanCount} spans`}
        </span>
        {group.costUsd > 0 && (
          <span className="shrink-0 font-mono text-[10px] text-amber-400/90 tabular-nums">
            {fmtUsd(group.costUsd)}
          </span>
        )}
        {group.errorCount > 0 && <AlertTriangle className="size-3 shrink-0 text-destructive" />}
      </div>
      <div className="relative hidden h-6 sm:block" style={gridStyle}>
        {visible && (
          <div
            className="absolute top-1 bottom-1 rounded-sm bg-foreground/[0.08]"
            style={{ left: `${leftPct}%`, width: `${Math.max(rightPct - leftPct, 0.25)}%` }}
          />
        )}
      </div>
      <div className="py-1 pr-3 text-right font-mono text-[11px] text-muted-foreground tabular-nums">
        {fmtDurationUs(Number(group.endUs - group.startUs))}
      </div>
    </div>
  );
});

const SpanRow = memo(function SpanRow({
  node,
  matched,
  selected,
  collapsed,
  minStartUs,
  view,
  gridStyle,
  onSelect,
  onToggleCollapse,
  onHover,
}: {
  node: WaterfallNode;
  matched: boolean;
  selected: boolean;
  collapsed: boolean;
  minStartUs: bigint;
  view: ViewWindow;
  gridStyle: CSSProperties;
  onSelect: (id: string) => void;
  onToggleCollapse: (id: string) => void;
  onHover: (node: WaterfallNode | null, e?: ReactPointerEvent) => void;
}) {
  const span = node.span;
  const kind = baseKind(span.kind);
  const meta = KIND_META[kind];
  const Icon = meta.icon;
  const geo = barGeometry(span, minStartUs, view);
  const tokens = kind === "llm" ? spanTokens(span) : undefined;
  const cost = spanCostUsd(span);
  const model = kind === "subagent" || kind === "session" ? spanModel(span) : undefined;

  return (
    <div
      className={cn(
        ROW_GRID,
        "group cursor-pointer transition-colors",
        selected ? "bg-primary/[0.08]" : "hover:bg-muted/40",
        span.isError && !selected && "bg-destructive/[0.05]",
        !matched && "opacity-50",
      )}
      onClick={() => onSelect(span.spanId)}
      onPointerMove={(e) => onHover(node, e)}
      onPointerLeave={() => onHover(null)}
    >
      {/* Name cell */}
      <div className="flex min-w-0 items-center gap-1.5 py-[3px] pr-2">
        {/* depth guides */}
        <div className="flex h-5 shrink-0 items-stretch" aria-hidden>
          {Array.from({ length: node.depth }).map((_, i) => (
            <span key={i} className="ml-[7px] w-[7px] border-l border-border/50" />
          ))}
        </div>
        {node.childCount > 0 ? (
          <button
            type="button"
            aria-label={collapsed ? "Expand" : "Collapse"}
            onClick={(e) => {
              e.stopPropagation();
              onToggleCollapse(span.spanId);
            }}
            className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <ChevronRight className={cn("size-3 transition-transform", !collapsed && "rotate-90")} />
          </button>
        ) : (
          <span className="w-4 shrink-0" />
        )}
        <Icon className={cn("size-3.5 shrink-0", span.isError ? "text-destructive" : meta.text)} />
        <span
          className={cn(
            "truncate text-xs",
            span.isError ? "text-destructive" : "text-foreground/90",
            kind === "llm" && "font-mono text-[11px]",
          )}
          title={span.operationName}
        >
          {spanDisplayName(span)}
        </span>
        {model && (
          <span className="hidden max-w-28 shrink-0 truncate font-mono text-[10px] text-muted-foreground lg:inline">
            {model}
          </span>
        )}
        {tokens && (
          <span className="hidden shrink-0 font-mono text-[10px] text-muted-foreground tabular-nums xl:inline">
            {fmtTokensCompact(tokens.input)}/{fmtTokensCompact(tokens.output)}
          </span>
        )}
        {cost !== undefined && (
          <span className="shrink-0 font-mono text-[10px] text-amber-400/90 tabular-nums">
            {fmtUsd(cost)}
          </span>
        )}
        {collapsed && node.descendantCount > 0 && (
          <span className="shrink-0 rounded-full bg-muted px-1.5 font-mono text-[9px] text-muted-foreground">
            +{node.descendantCount}
          </span>
        )}
        {(span.isError || node.descendantErrors > 0) && (
          <AlertTriangle className="size-3 shrink-0 text-destructive" />
        )}
      </div>

      {/* Timeline cell */}
      <div className="relative hidden h-6 border-l border-border/30 sm:block" style={gridStyle}>
        {geo && (
          <div
            className={cn(
              "absolute top-[5px] h-[14px] rounded-[3px]",
              span.isError ? "bg-destructive" : meta.bar,
              "opacity-70 group-hover:opacity-100",
              selected && "opacity-100 ring-1 ring-foreground/40",
            )}
            style={{ left: `${geo.leftPct}%`, width: `${geo.widthPct}%`, minWidth: "2px" }}
          />
        )}
      </div>

      {/* Duration cell */}
      <div
        className={cn(
          "py-[3px] pr-3 text-right font-mono text-[11px] tabular-nums",
          span.isError ? "text-destructive" : "text-muted-foreground",
        )}
      >
        {fmtDurationUs(span.durationUs)}
      </div>
    </div>
  );
});

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function TraceWaterfallView({ trace }: { trace: GetAgentTraceResponse }) {
  const wf = useMemo(() => buildWaterfall(trace.spans), [trace.spans]);

  // --- filters -------------------------------------------------------------
  const [query, setQuery] = useState("");
  const [kinds, setKinds] = useState<Set<BaseKind> | null>(null);
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [groupTurns, setGroupTurns] = useState(true);

  // --- collapse ------------------------------------------------------------
  const [collapsedSpans, setCollapsedSpans] = useState<ReadonlySet<string>>(new Set());
  const [collapsedGroups, setCollapsedGroups] = useState<ReadonlySet<string>>(new Set());
  const [manualGroups, setManualGroups] = useState<ReadonlySet<string>>(new Set());

  // --- zoom ----------------------------------------------------------------
  const fullView = useMemo<ViewWindow>(
    () => ({ startUs: 0, endUs: Math.max(Number(wf.maxEndUs - wf.minStartUs), 1) }),
    [wf.maxEndUs, wf.minStartUs],
  );
  const [zoom, setZoom] = useState<ViewWindow | null>(null);
  const view = zoom ?? fullView;

  const rulerRef = useRef<HTMLDivElement>(null);
  const [brush, setBrush] = useState<{ a: number; b: number } | null>(null);
  const brushPct = useCallback((clientX: number) => {
    const el = rulerRef.current;
    if (!el) return 0;
    const rect = el.getBoundingClientRect();
    return Math.min(Math.max(((clientX - rect.left) / rect.width) * 100, 0), 100);
  }, []);

  // --- selection + hover ---------------------------------------------------
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selectedNode = selectedId ? (wf.byId.get(selectedId) ?? null) : null;
  const [hoverTip, setHoverTip] = useState<HoverTip>(null);

  // --- render-phase state adjustments ---------------------------------------
  // (recommended over effects for prop-driven resets; avoids cascading renders)
  // Reset per-trace UI state when a different trace arrives, and auto-collapse
  // finished turns (all but the last) as new turns appear, respecting toggles
  // the user made manually.
  const [prevTraceId, setPrevTraceId] = useState(trace.traceId);
  const lastGroupKey = wf.groups.length > 0 ? wf.groups[wf.groups.length - 1].key : "";
  const [prevLastGroup, setPrevLastGroup] = useState("");
  if (prevTraceId !== trace.traceId) {
    setPrevTraceId(trace.traceId);
    setPrevLastGroup("");
    setManualGroups(new Set());
    setCollapsedSpans(new Set());
    setCollapsedGroups(new Set());
    setZoom(null);
    setSelectedId(null);
  } else if (prevLastGroup !== lastGroupKey) {
    setPrevLastGroup(lastGroupKey);
    if (lastGroupKey !== "") {
      setCollapsedGroups((prev) => {
        const next = new Set<string>();
        for (const g of wf.groups) {
          if (manualGroups.has(g.key)) {
            if (prev.has(g.key)) next.add(g.key);
          } else if (g.key !== lastGroupKey) {
            next.add(g.key);
          }
        }
        if (next.size === prev.size && [...next].every((k) => prev.has(k))) return prev;
        return next;
      });
    }
  }

  // --- rows ----------------------------------------------------------------
  const filter = useMemo<RowFilter>(() => ({ query: query.trim(), kinds, errorsOnly }), [query, kinds, errorsOnly]);
  const rows = useMemo(
    () =>
      assembleRows(wf, {
        filter,
        collapsedSpans,
        groupTurns,
        collapsedGroups,
      }),
    [wf, filter, collapsedSpans, groupTurns, collapsedGroups],
  );
  const shownRows = rows.length > RENDER_CAP ? rows.slice(0, RENDER_CAP) : rows;

  const kindCounts = useMemo(() => {
    const counts = new Map<BaseKind, number>();
    for (const n of wf.nodes) {
      const k = baseKind(n.span.kind);
      counts.set(k, (counts.get(k) ?? 0) + 1);
    }
    return counts;
  }, [wf.nodes]);
  const presentKinds = KIND_ORDER.filter((k) => (kindCounts.get(k) ?? 0) > 0);

  const toggleKind = (k: BaseKind) => {
    setKinds((prev) => {
      const next = new Set(prev ?? presentKinds);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      if (next.size === presentKinds.length) return null;
      return next;
    });
  };

  const toggleGroup = useCallback((key: string) => {
    setManualGroups((prev) => new Set(prev).add(key));
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const toggleSpanCollapse = useCallback((id: string) => {
    setCollapsedSpans((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const selectSpan = useCallback((id: string) => {
    setSelectedId((prev) => (prev === id ? null : id));
  }, []);

  const handleHover = useCallback((node: WaterfallNode | null, e?: ReactPointerEvent) => {
    if (!node || !e) setHoverTip(null);
    else setHoverTip({ x: e.clientX, y: e.clientY, node });
  }, []);

  const collapseAll = () => {
    const spans = new Set<string>();
    for (const n of wf.nodes) if (n.childCount > 0) spans.add(n.span.spanId);
    setCollapsedSpans(spans);
    setManualGroups(new Set(wf.groups.map((g) => g.key)));
    setCollapsedGroups(new Set(wf.groups.map((g) => g.key)));
  };
  const expandAll = () => {
    setCollapsedSpans(new Set());
    setManualGroups(new Set(wf.groups.map((g) => g.key)));
    setCollapsedGroups(new Set());
  };

  // --- ticks + gridlines ---------------------------------------------------
  const range = view.endUs - view.startUs;
  const ticks = useMemo(() => computeTicks(range), [range]);
  const stepPct = ticks.length > 1 ? ((ticks[1].offsetUs - ticks[0].offsetUs) / range) * 100 : 100;
  const gridStyle = useMemo<CSSProperties>(
    () => ({ backgroundImage: GRIDLINE_BG, backgroundSize: `${stepPct}% 100%` }),
    [stepPct],
  );

  if (wf.nodes.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
        <CircleDot className="mb-2 size-5 animate-pulse" />
        <p className="text-sm font-medium text-foreground">No trace spans yet</p>
        <p className="mt-1 text-xs">Spans will appear once the agent emits tracing data.</p>
      </div>
    );
  }

  return (
    <div
      className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border/60 bg-background"
      onKeyDown={(e) => {
        if (e.key === "Escape") setSelectedId(null);
      }}
    >
      <SummaryHeader wf={wf} trace={trace} />

      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 border-b border-border/50 px-3 py-1.5">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2 size-3 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search spans…"
            className="h-6 w-40 pl-6 text-xs md:w-52"
          />
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {presentKinds.map((k) => {
            const active = kinds === null || kinds.has(k);
            const meta = KIND_META[k];
            return (
              <button
                key={k}
                type="button"
                onClick={() => toggleKind(k)}
                aria-pressed={active}
                className={cn(
                  "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-medium transition-colors",
                  active
                    ? "border-border bg-muted/60 text-foreground"
                    : "border-transparent text-muted-foreground/60 hover:text-muted-foreground",
                )}
              >
                <span className={cn("size-1.5 rounded-full", meta.dot, !active && "opacity-40")} />
                {meta.label}
                <span className="font-mono tabular-nums">{kindCounts.get(k)}</span>
              </button>
            );
          })}
          <button
            type="button"
            onClick={() => setErrorsOnly((v) => !v)}
            aria-pressed={errorsOnly}
            className={cn(
              "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-medium transition-colors",
              errorsOnly
                ? "border-destructive/40 bg-destructive/10 text-destructive"
                : "border-transparent text-muted-foreground/60 hover:text-muted-foreground",
            )}
          >
            <AlertTriangle className="size-2.5" />
            Errors
          </button>
        </div>
        <span className="ml-auto flex items-center gap-2">
          {zoom && (
            <button
              type="button"
              onClick={() => setZoom(null)}
              className="inline-flex items-center gap-1 rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-[10px] font-medium text-primary transition-colors hover:bg-primary/20"
            >
              <RotateCcw className="size-2.5" />
              Reset zoom
            </button>
          )}
          {wf.groups.length > 0 && (
            <label className="flex cursor-pointer items-center gap-1.5 text-[10px] text-muted-foreground">
              <Switch checked={groupTurns} onCheckedChange={setGroupTurns} aria-label="Group turns" />
              Turns
            </label>
          )}
          <button
            type="button"
            onClick={expandAll}
            title="Expand all"
            className="rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <ChevronsUpDown className="size-3.5" />
          </button>
          <button
            type="button"
            onClick={collapseAll}
            title="Collapse all"
            className="rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <ChevronsDownUp className="size-3.5" />
          </button>
        </span>
      </div>

      {/* Scrollable waterfall */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {/* Ruler */}
        <div className={cn(ROW_GRID, "sticky top-0 z-10 border-b border-border/60 bg-background/95 backdrop-blur-sm")}>
          <div className="flex items-center justify-between py-1 pr-2 pl-2">
            <span className="text-[10px] text-muted-foreground">
              {rows.length.toLocaleString()} rows
            </span>
            <span className="hidden text-[9px] text-muted-foreground/60 italic sm:inline">
              drag to zoom
            </span>
          </div>
          <div
            ref={rulerRef}
            className="relative hidden h-7 cursor-crosshair touch-none border-l border-border/30 select-none sm:block"
            style={gridStyle}
            onDoubleClick={() => setZoom(null)}
            onPointerDown={(e) => {
              e.currentTarget.setPointerCapture(e.pointerId);
              const p = brushPct(e.clientX);
              setBrush({ a: p, b: p });
            }}
            onPointerMove={(e) => {
              if (brush) setBrush({ a: brush.a, b: brushPct(e.clientX) });
            }}
            onPointerUp={() => {
              if (!brush) return;
              const lo = Math.min(brush.a, brush.b);
              const hi = Math.max(brush.a, brush.b);
              setBrush(null);
              if (hi - lo < 1) return; // click, not a drag
              const startUs = view.startUs + (lo / 100) * range;
              const endUs = view.startUs + (hi / 100) * range;
              if (endUs - startUs >= 100) setZoom({ startUs, endUs });
            }}
          >
            {ticks.map((t) => (
              <span
                key={t.offsetUs}
                className="absolute top-1/2 -translate-y-1/2 pl-1 font-mono text-[9px] text-muted-foreground tabular-nums"
                style={{ left: `${(t.offsetUs / range) * 100}%` }}
              >
                {fmtOffsetUs(view.startUs + t.offsetUs)}
              </span>
            ))}
            {brush && (
              <div
                className="absolute inset-y-0 bg-primary/20 ring-1 ring-primary/50"
                style={{
                  left: `${Math.min(brush.a, brush.b)}%`,
                  width: `${Math.abs(brush.b - brush.a)}%`,
                }}
              />
            )}
          </div>
          <div className="py-1 pr-3 text-right text-[9px] text-muted-foreground/60">duration</div>
        </div>

        {/* Rows */}
        <div className="divide-y divide-border/15">
          {shownRows.map((row: WaterfallRow) =>
            row.type === "group" ? (
              <GroupRow
                key={row.group.key}
                group={row.group}
                collapsed={collapsedGroups.has(row.group.key)}
                onToggle={toggleGroup}
                minStartUs={wf.minStartUs}
                view={view}
                gridStyle={gridStyle}
              />
            ) : (
              <SpanRow
                key={row.node.span.spanId}
                node={row.node}
                matched={row.matched}
                selected={selectedId === row.node.span.spanId}
                collapsed={collapsedSpans.has(row.node.span.spanId)}
                minStartUs={wf.minStartUs}
                view={view}
                gridStyle={gridStyle}
                onSelect={selectSpan}
                onToggleCollapse={toggleSpanCollapse}
                onHover={handleHover}
              />
            ),
          )}
        </div>
        {rows.length > RENDER_CAP && (
          <p className="px-3 py-2 text-center text-[11px] text-muted-foreground">
            Showing first {RENDER_CAP.toLocaleString()} of {rows.length.toLocaleString()} rows — narrow
            with search or filters.
          </p>
        )}
        {rows.length === 0 && (
          <p className="px-3 py-8 text-center text-[11px] text-muted-foreground">
            No spans match the current filters.
          </p>
        )}
      </div>

      {selectedNode && (
        <DetailPanel node={selectedNode} minStartUs={wf.minStartUs} onClose={() => setSelectedId(null)} />
      )}
      <HoverCard tip={hoverTip} minStartUs={wf.minStartUs} />
    </div>
  );
}
