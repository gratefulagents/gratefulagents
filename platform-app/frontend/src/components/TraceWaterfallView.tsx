import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import type { GetAgentTraceResponse } from "@/rpc/platform/service_pb";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Skeleton } from "@/components/ui/skeleton";
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
  KIND_TOKEN,
  spanCostUsd,
  spanDisplayName,
  spanModel,
  spanTokens,
  type BaseKind,
  type KindToken,
  type RowFilter,
  type TurnGroup,
  type ViewWindow,
  type Waterfall,
  type WaterfallNode,
  type WaterfallRow,
} from "@/lib/traceWaterfall";
import { toneText } from "@/lib/status";
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
  /** Raw CSS colour for canvas/inline use. */
  color: string;
};

// Tailwind needs literal class names, so the eight tokens are spelled out.
const TOKEN_CLASSES: Record<KindToken, { text: string; bg: string; color: string }> = {
  llm: { text: "text-kind-llm", bg: "bg-kind-llm", color: "var(--kind-llm)" },
  tool: { text: "text-kind-tool", bg: "bg-kind-tool", color: "var(--kind-tool)" },
  agent: { text: "text-kind-agent", bg: "bg-kind-agent", color: "var(--kind-agent)" },
  subagent: { text: "text-kind-subagent", bg: "bg-kind-subagent", color: "var(--kind-subagent)" },
  session: { text: "text-kind-session", bg: "bg-kind-session", color: "var(--kind-session)" },
  handoff: { text: "text-kind-handoff", bg: "bg-kind-handoff", color: "var(--kind-handoff)" },
  control: { text: "text-kind-control", bg: "bg-kind-control", color: "var(--kind-control)" },
  other: { text: "text-kind-other", bg: "bg-kind-other", color: "var(--kind-other)" },
};

const KIND_LABEL_ICON: Record<BaseKind, { label: string; icon: typeof Zap }> = {
  llm: { label: "LLM", icon: Brain },
  tool: { label: "Tool", icon: Terminal },
  subagent: { label: "Subagent", icon: Bot },
  agent: { label: "Agent", icon: Bot },
  session: { label: "Session", icon: Cpu },
  handoff: { label: "Handoff", icon: GitBranch },
  guardrail: { label: "Guardrail", icon: ShieldAlert },
  compaction: { label: "Compaction", icon: Layers },
  retry: { label: "Retry", icon: RotateCcw },
  phase: { label: "Phase", icon: Layers },
  other: { label: "Other", icon: Zap },
};

const KIND_META: Record<BaseKind, KindMeta> = Object.fromEntries(
  KIND_ORDER.map((k) => {
    const { token, muted } = KIND_TOKEN[k];
    const t = TOKEN_CLASSES[token];
    return [
      k,
      {
        ...KIND_LABEL_ICON[k],
        text: t.text,
        bar: cn(t.bg, muted && "saturate-50"),
        dot: cn(t.bg, muted && "opacity-60"),
        color: t.color,
      },
    ];
  }),
) as Record<BaseKind, KindMeta>;

// Shared row grid: name | timeline | duration.
//
// The waterfall lives inside the run inspector, a ~380-600px panel, so the
// layout reacts to its *container* rather than the viewport. Widths are
// measured, and the timeline column is dropped entirely when there is not
// enough room for it to say anything.
type WidthTier = "narrow" | "medium" | "wide";

function widthTier(width: number): WidthTier {
  if (width === 0) return "medium"; // pre-measurement: the inspector's common case
  if (width < 340) return "narrow";
  if (width < 720) return "medium";
  return "wide";
}

const ROW_GRID_BY_TIER: Record<WidthTier, string> = {
  narrow: "grid grid-cols-[minmax(0,1fr)_56px]",
  medium: "grid grid-cols-[minmax(132px,180px)_minmax(0,1fr)_52px]",
  wide: "grid grid-cols-[minmax(230px,330px)_minmax(0,1fr)_64px]",
};

/** Observes an element's own width so layout can react to the pane, not the page. */
function useElementWidth(ref: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const next = entries[0]?.contentRect.width ?? 0;
      setWidth((prev) => (Math.abs(prev - next) < 1 ? prev : next));
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [ref]);
  return width;
}

const GRIDLINE_BG =
  "linear-gradient(to right, color-mix(in oklch, var(--border) 60%, transparent) 1px, transparent 1px)";

const RENDER_CAP = 1500;
const ROW_HEIGHT = 24;
const MIN_ZOOM_US = 100;

const BAR_MOTION = "transition-[left,width,opacity] duration-[var(--dur-base)] ease-[var(--ease-out-quart)]";

function rowId(row: WaterfallRow): string {
  return row.type === "group" ? `g:${row.group.key}` : `s:${row.node.span.spanId}`;
}

// ---------------------------------------------------------------------------
// Shared hover tooltip (single instance — cheap for large traces)
// ---------------------------------------------------------------------------

type HoverTip = { x: number; y: number; node: WaterfallNode } | null;

const HOVER_CARD_HEIGHT = 150;

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
      style={{
        left: Math.max(Math.min(tip.x + 12, window.innerWidth - 320), 8),
        top: Math.max(Math.min(tip.y + 14, window.innerHeight - HOVER_CARD_HEIGHT), 8),
      }}
    >
      <div className="flex items-center gap-1.5 text-xs font-medium">
        <span className={cn("size-2 rounded-full", span.isError ? "bg-tone-danger" : meta.dot)} />
        <span className="truncate">{spanDisplayName(span)}</span>
        <span className="text-muted-foreground">· {meta.label}</span>
      </div>
      <div className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono text-2xs text-muted-foreground tabular-nums">
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
            <span className={toneText.warning}>{fmtUsd(cost)}</span>
          </>
        )}
      </div>
      {span.isError && (
        <p className={cn("mt-1 line-clamp-3 break-all text-2xs", toneText.danger)}>{errText || "error"}</p>
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
      <span className="font-mono text-2xs tabular-nums">{children}</span>
    </span>
  );
}

function SummaryHeader({ wf, trace, tier }: { wf: Waterfall; trace: GetAgentTraceResponse; tier: WidthTier }) {
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
      <span className="font-mono text-2xs text-muted-foreground tabular-nums">
        {wf.nodes.length.toLocaleString()} spans
      </span>
      {stats.llm > 0 && (
        <StatItem icon={Brain} className={KIND_META.llm.text}>
          {stats.llm} LLM · {fmtDurationUs(stats.llmUs)}
        </StatItem>
      )}
      {stats.tools > 0 && (
        <StatItem icon={Terminal} className={KIND_META.tool.text}>
          {stats.tools} tools · {fmtDurationUs(stats.toolUs)}
        </StatItem>
      )}
      {stats.subagents > 0 && (
        <StatItem icon={Bot} className={KIND_META.subagent.text}>
          {stats.subagents} subagents
        </StatItem>
      )}
      {usage.hasUsage && (
        <StatItem icon={Hash} className="text-muted-foreground">
          {fmtTokensCompact(usage.inputTokens)} in / {fmtTokensCompact(usage.outputTokens)} out
        </StatItem>
      )}
      {usage.hasCost && (
        <StatItem icon={Coins} className={toneText.warning}>
          {fmtUsd(usage.costUsd)}
        </StatItem>
      )}
      {stats.errors > 0 && (
        <StatItem icon={AlertTriangle} className={toneText.danger}>
          {stats.errors} {stats.errors === 1 ? "error" : "errors"}
        </StatItem>
      )}
      <span className="ml-auto inline-flex items-center gap-3">
        {trace.serviceName && tier === "wide" && (
          <span className="font-mono text-3xs text-muted-foreground">{trace.serviceName}</span>
        )}
        {!trace.isComplete && (
          <span className={cn("inline-flex items-center gap-1", toneText.running)}>
            <CircleDot className="size-2.5 animate-pulse" />
            <span className="text-3xs font-medium">LIVE</span>
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
  const [copied, setCopied] = useState<"tags" | "id" | null>(null);

  const copyText = (what: "tags" | "id", text: string) => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(what);
      setTimeout(() => setCopied(null), 1200);
    });
  };
  const copyTags = () =>
    copyText(
      "tags",
      JSON.stringify(
        {
          name: span.operationName,
          kind: span.kind,
          spanId: span.spanId,
          startOffset: fmtOffsetUs(offsetUs),
          duration: fmtDurationUs(span.durationUs),
          tags: Object.fromEntries(span.tags.map((t) => [t.key, t.value])),
        },
        null,
        2,
      ),
    );

  const actionClass =
    "inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-3xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground";

  return (
    <div className="flex max-h-72 shrink-0 flex-col border-t border-border bg-muted/20">
      <div className="flex items-center gap-2 border-b border-border/50 px-3 py-1.5">
        <Icon className={cn("size-3.5 shrink-0", span.isError ? toneText.danger : meta.text)} />
        <span className="min-w-0 truncate text-xs font-medium text-foreground">{span.operationName}</span>
        <span className="shrink-0 font-mono text-3xs text-muted-foreground">{meta.label}</span>
        {span.isError && <AlertTriangle className={cn("size-3 shrink-0", toneText.danger)} />}
        <span className="ml-auto flex shrink-0 items-center gap-2 font-mono text-3xs text-muted-foreground tabular-nums">
          <span>+{fmtOffsetUs(offsetUs)}</span>
          <span className="text-foreground">{fmtDurationUs(span.durationUs)}</span>
        </span>
        <button type="button" onClick={() => copyText("id", span.spanId)} className={actionClass} title={span.spanId}>
          <Hash className="size-3" />
          {copied === "id" ? "Copied" : "Copy span id"}
        </button>
        <button type="button" onClick={copyTags} className={actionClass}>
          <Copy className="size-3" />
          {copied === "tags" ? "Copied" : "Copy"}
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
          <p className="text-2xs text-muted-foreground">No attributes.</p>
        ) : (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
            {tags.map((t) => (
              <div key={t.key} className="contents">
                <dt className="font-mono text-2xs whitespace-nowrap text-muted-foreground">{t.key}</dt>
                <dd className="min-w-0 font-mono text-2xs break-all whitespace-pre-wrap text-foreground">{t.value}</dd>
              </div>
            ))}
          </dl>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Minimap
// ---------------------------------------------------------------------------

const MINIMAP_BANDS: KindToken[] = ["session", "agent", "subagent", "handoff", "llm", "tool", "control", "other"];

function Minimap({
  wf,
  fullView,
  view,
  onChangeView,
}: {
  wf: Waterfall;
  fullView: ViewWindow;
  view: ViewWindow;
  onChangeView: (v: ViewWindow | null) => void;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const dragRef = useRef<{ startFrac: number; mode: "pan" | "brush"; viewAtStart: ViewWindow } | null>(null);
  const [brush, setBrush] = useState<{ a: number; b: number } | null>(null);
  const fullRange = fullView.endUs - fullView.startUs;

  const frac = (clientX: number) => {
    const el = canvasRef.current;
    if (!el) return 0;
    const rect = el.getBoundingClientRect();
    return Math.min(Math.max((clientX - rect.left) / rect.width, 0), 1);
  };

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const cssW = canvas.clientWidth || 300;
    const cssH = canvas.clientHeight || 24;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(cssW * dpr);
    canvas.height = Math.round(cssH * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, cssW, cssH);

    const style = getComputedStyle(canvas);
    const colorFor = (token: KindToken) =>
      style.getPropertyValue(`--kind-${token}`).trim() || "rgb(148 163 184)";
    const dangerColor = style.getPropertyValue("--tone-danger").trim() || "rgb(239 68 68)";
    const fgColor = style.getPropertyValue("--foreground").trim() || "rgb(255 255 255)";

    const bandH = cssH / MINIMAP_BANDS.length;
    ctx.globalAlpha = 0.55;
    for (const n of wf.nodes) {
      const start = Number(n.span.startTimeUnixUs - wf.minStartUs);
      const x0 = (start / fullRange) * cssW;
      const x1 = Math.max(((start + Number(n.span.durationUs)) / fullRange) * cssW, x0 + 1);
      const token = KIND_TOKEN[baseKind(n.span.kind)].token;
      ctx.fillStyle = colorFor(token);
      const band = MINIMAP_BANDS.indexOf(token);
      ctx.fillRect(x0, band * bandH + 0.5, x1 - x0, Math.max(bandH - 1, 1));
    }
    ctx.globalAlpha = 0.9;
    ctx.fillStyle = dangerColor;
    for (const n of wf.nodes) {
      if (!n.span.isError) continue;
      const x = (Number(n.span.startTimeUnixUs - wf.minStartUs) / fullRange) * cssW;
      ctx.fillRect(Math.floor(x), 0, 1, cssH);
    }

    const vx0 = ((view.startUs - fullView.startUs) / fullRange) * cssW;
    const vx1 = ((view.endUs - fullView.startUs) / fullRange) * cssW;
    if (vx0 > 0 || vx1 < cssW) {
      ctx.globalAlpha = 0.14;
      ctx.fillStyle = fgColor;
      ctx.fillRect(vx0, 0, vx1 - vx0, cssH);
      ctx.globalAlpha = 0.6;
      ctx.fillRect(vx0, 0, 1, cssH);
      ctx.fillRect(vx1 - 1, 0, 1, cssH);
    }
    if (brush) {
      const bx0 = Math.min(brush.a, brush.b) * cssW;
      const bx1 = Math.max(brush.a, brush.b) * cssW;
      ctx.globalAlpha = 0.25;
      ctx.fillStyle = fgColor;
      ctx.fillRect(bx0, 0, bx1 - bx0, cssH);
    }
    ctx.globalAlpha = 1;
  }, [wf, fullView, fullRange, view, brush]);

  const isZoomed = view.startUs > fullView.startUs || view.endUs < fullView.endUs;

  return (
    <canvas
      ref={canvasRef}
      role="img"
      aria-label="Trace minimap. Drag to zoom or pan the timeline."
      className="block h-6 w-full cursor-crosshair touch-none select-none"
      onDoubleClick={() => onChangeView(null)}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId);
        const f = frac(e.clientX);
        const inWindow =
          isZoomed &&
          f * fullRange + fullView.startUs >= view.startUs &&
          f * fullRange + fullView.startUs <= view.endUs;
        dragRef.current = { startFrac: f, mode: inWindow ? "pan" : "brush", viewAtStart: view };
        if (!inWindow) setBrush({ a: f, b: f });
      }}
      onPointerMove={(e) => {
        const drag = dragRef.current;
        if (!drag) return;
        const f = frac(e.clientX);
        if (drag.mode === "brush") {
          setBrush({ a: drag.startFrac, b: f });
          return;
        }
        const range = drag.viewAtStart.endUs - drag.viewAtStart.startUs;
        let start = drag.viewAtStart.startUs + (f - drag.startFrac) * fullRange;
        start = Math.min(Math.max(start, fullView.startUs), fullView.endUs - range);
        onChangeView({ startUs: start, endUs: start + range });
      }}
      onPointerUp={(e) => {
        const drag = dragRef.current;
        dragRef.current = null;
        if (!drag || drag.mode !== "brush") return;
        const f = frac(e.clientX);
        setBrush(null);
        const lo = Math.min(drag.startFrac, f);
        const hi = Math.max(drag.startFrac, f);
        if (hi - lo < 0.01) return;
        const startUs = fullView.startUs + lo * fullRange;
        const endUs = fullView.startUs + hi * fullRange;
        if (endUs - startUs >= MIN_ZOOM_US) onChangeView({ startUs, endUs });
      }}
      onPointerCancel={() => {
        dragRef.current = null;
        setBrush(null);
      }}
    />
  );
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

type RowA11y = {
  id: string;
  index: number;
  level: number;
  tabIndex: 0 | -1;
};

const GroupRow = memo(function GroupRow({
  group,
  collapsed,
  onToggle,
  minStartUs,
  view,
  gridStyle,
  gridClass,
  showTimeline,
  a11y,
}: {
  group: TurnGroup;
  collapsed: boolean;
  onToggle: (key: string) => void;
  minStartUs: bigint;
  view: ViewWindow;
  gridStyle: CSSProperties;
  gridClass: string;
  showTimeline: boolean;
  a11y: RowA11y;
}) {
  const range = view.endUs - view.startUs;
  const start = Number(group.startUs - minStartUs);
  const end = Number(group.endUs - minStartUs);
  const leftPct = Math.max(((start - view.startUs) / range) * 100, 0);
  const rightPct = Math.min(((end - view.startUs) / range) * 100, 100);
  const visible = rightPct > 0 && leftPct < 100;
  return (
    <div
      className={cn(
        gridClass,
        "group h-6 cursor-pointer bg-muted/30 outline-none hover:bg-muted/50 focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-ring",
      )}
      onClick={() => onToggle(group.key)}
      role="treeitem"
      aria-expanded={!collapsed}
      aria-selected={false}
      aria-level={a11y.level}
      tabIndex={a11y.tabIndex}
      data-row-id={a11y.id}
      data-row-index={a11y.index}
    >
      <div className="flex min-w-0 items-center gap-1.5 pr-2 pl-1.5">
        {collapsed ? (
          <ChevronRight className="size-3 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
        )}
        <span className="text-2xs font-semibold tracking-wide text-foreground uppercase">{group.label}</span>
        <span className="min-w-0 truncate font-mono text-3xs text-muted-foreground tabular-nums">
          {group.genCount > 0 && `${group.genCount} llm`}
          {group.toolCount > 0 && ` · ${group.toolCount} tools`}
          {collapsed && group.spanCount > 0 && ` · ${group.spanCount} spans`}
        </span>
        {group.costUsd > 0 && (
          <span className={cn("shrink-0 font-mono text-3xs tabular-nums", toneText.warning)}>
            {fmtUsd(group.costUsd)}
          </span>
        )}
        {group.errorCount > 0 && <AlertTriangle className={cn("size-3 shrink-0", toneText.danger)} />}
      </div>
      <div className={cn("relative h-6", showTimeline ? "block" : "hidden")} style={gridStyle}>
        {visible && (
          <div
            className={cn("absolute top-1 bottom-1 rounded-sm bg-foreground/[0.08]", BAR_MOTION)}
            style={{ left: `${leftPct}%`, width: `${Math.max(rightPct - leftPct, 0.25)}%` }}
          />
        )}
      </div>
      <div className="pr-3 text-right font-mono text-2xs leading-6 text-muted-foreground tabular-nums">
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
  gridClass,
  showTimeline,
  tier,
  a11y,
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
  gridClass: string;
  showTimeline: boolean;
  tier: WidthTier;
  a11y: RowA11y;
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
  const name = spanDisplayName(span);

  return (
    <div
      className={cn(
        gridClass,
        "group h-6 cursor-pointer outline-none transition-colors focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-ring",
        selected ? "bg-primary/[0.08]" : "hover:bg-muted/40",
        span.isError && !selected && "bg-[color-mix(in_oklch,var(--tone-danger)_6%,transparent)]",
        !matched && "opacity-50",
      )}
      onClick={() => onSelect(span.spanId)}
      onPointerMove={(e) => onHover(node, e)}
      onPointerLeave={() => onHover(null)}
      role="treeitem"
      aria-selected={selected}
      aria-expanded={node.childCount > 0 ? !collapsed : undefined}
      aria-level={a11y.level}
      tabIndex={a11y.tabIndex}
      data-row-id={a11y.id}
      data-row-index={a11y.index}
    >
      {/* Name cell */}
      <div className="flex min-w-0 items-center gap-1.5 pr-2">
        <div className="flex h-5 shrink-0 items-stretch" aria-hidden>
          {Array.from({ length: node.depth }).map((_, i) => (
            <span key={i} className="ml-[7px] w-[7px] border-l border-border/50" />
          ))}
        </div>
        {node.childCount > 0 ? (
          <button
            type="button"
            tabIndex={-1}
            aria-label={`${collapsed ? "Expand" : "Collapse"} ${name}`}
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
        <Icon className={cn("size-3.5 shrink-0", span.isError ? toneText.danger : meta.text)} />
        <span
          className={cn(
            "min-w-0 truncate text-xs",
            span.isError ? toneText.danger : "text-foreground/90",
            kind === "llm" && "font-mono text-2xs",
          )}
        >
          {name}
        </span>
        {model && tier === "wide" && (
          <span className="max-w-28 shrink-0 truncate font-mono text-3xs text-muted-foreground">{model}</span>
        )}
        {tokens && tier === "wide" && (
          <span className="shrink-0 font-mono text-3xs text-muted-foreground tabular-nums">
            {fmtTokensCompact(tokens.input)}/{fmtTokensCompact(tokens.output)}
          </span>
        )}
        {cost !== undefined && (
          <span className={cn("shrink-0 font-mono text-3xs tabular-nums", toneText.warning)}>{fmtUsd(cost)}</span>
        )}
        {collapsed && node.descendantCount > 0 && (
          <span className="shrink-0 rounded-full bg-muted px-1.5 font-mono text-[9px] text-muted-foreground">
            +{node.descendantCount}
          </span>
        )}
        {(span.isError || node.descendantErrors > 0) && (
          <AlertTriangle className={cn("size-3 shrink-0", toneText.danger)} />
        )}
      </div>

      {/* Timeline cell */}
      <div className={cn("relative h-6 border-l border-border/30", showTimeline ? "block" : "hidden")} style={gridStyle}>
        {geo && (
          <div
            className={cn(
              "absolute top-[5px] h-[14px] rounded-[3px]",
              BAR_MOTION,
              span.isError ? "bg-tone-danger" : meta.bar,
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
          "pr-3 text-right font-mono text-2xs leading-6 tabular-nums",
          span.isError ? toneText.danger : "text-muted-foreground",
        )}
      >
        {fmtDurationUs(span.durationUs)}
      </div>
    </div>
  );
});

// ---------------------------------------------------------------------------
// Loading skeleton
// ---------------------------------------------------------------------------

export function TraceWaterfallSkeleton({ className }: { className?: string }) {
  return (
    <div className={cn("flex flex-col gap-1 p-3", className)} aria-busy="true" aria-label="Loading trace">
      <div className="mb-2 flex gap-3">
        <Skeleton className="h-3 w-16" />
        <Skeleton className="h-3 w-20" />
        <Skeleton className="h-3 w-12" />
      </div>
      {Array.from({ length: 8 }).map((_, i) => (
        <div key={i} className="grid h-6 grid-cols-[minmax(120px,180px)_minmax(0,1fr)_48px] items-center gap-2">
          <Skeleton className="h-3" style={{ width: `${60 + ((i * 17) % 35)}%` }} />
          <Skeleton className="h-3.5" style={{ marginLeft: `${(i * 11) % 60}%`, width: `${12 + ((i * 23) % 30)}%` }} />
          <Skeleton className="h-3 w-full" />
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function TraceWaterfallView({
  trace,
  className,
}: {
  trace: GetAgentTraceResponse;
  /** Overrides the default card chrome when the host already frames the view. */
  className?: string;
}) {
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

  // --- container-driven layout -------------------------------------------
  const rootRef = useRef<HTMLDivElement>(null);
  const containerWidth = useElementWidth(rootRef);
  const tier = widthTier(containerWidth);
  const gridClass = ROW_GRID_BY_TIER[tier];
  const showTimeline = tier !== "narrow";

  const rulerRef = useRef<HTMLDivElement>(null);
  const [brush, setBrush] = useState<{ a: number; b: number } | null>(null);
  const brushPct = useCallback((clientX: number) => {
    const el = rulerRef.current;
    if (!el) return 0;
    const rect = el.getBoundingClientRect();
    return Math.min(Math.max(((clientX - rect.left) / rect.width) * 100, 0), 100);
  }, []);

  // --- selection + hover + focus -----------------------------------------
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selectedNode = selectedId ? (wf.byId.get(selectedId) ?? null) : null;
  const [hoverTip, setHoverTip] = useState<HoverTip>(null);
  const [focusedRowId, setFocusedRowId] = useState<string | null>(null);
  const pendingFocusRef = useRef(false);
  const virtuosoRef = useRef<VirtuosoHandle>(null);

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
    setFocusedRowId(null);
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
  // A live trace grows at the end, so the cap keeps the newest rows; a finished
  // trace reads top-down, so it keeps the first ones.
  const capped = rows.length > RENDER_CAP;
  const shownRows = useMemo(
    () => (capped ? (trace.isComplete ? rows.slice(0, RENDER_CAP) : rows.slice(-RENDER_CAP)) : rows),
    [rows, capped, trace.isComplete],
  );
  const grouped = groupTurns && wf.groups.length > 0;
  const firstRowId = shownRows.length > 0 ? rowId(shownRows[0]) : null;
  const activeRowId = focusedRowId ?? (selectedId ? `s:${selectedId}` : null) ?? firstRowId;

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
    if (!node || !e || e.pointerType === "touch") setHoverTip(null);
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

  // --- keyboard navigation -------------------------------------------------
  const focusRow = useCallback(
    (index: number) => {
      const row = shownRows[index];
      if (!row) return;
      setFocusedRowId(rowId(row));
      pendingFocusRef.current = true;
      virtuosoRef.current?.scrollIntoView({ index, behavior: "auto" });
    },
    [shownRows],
  );

  useEffect(() => {
    if (!pendingFocusRef.current || !focusedRowId) return;
    let frame = 0;
    let attempts = 0;
    const tryFocus = () => {
      const root = rootRef.current;
      if (!root) return;
      const el = Array.from(root.querySelectorAll<HTMLElement>("[data-row-id]")).find(
        (n) => n.dataset.rowId === focusedRowId,
      );
      if (el) {
        pendingFocusRef.current = false;
        el.focus({ preventScroll: true });
      } else if (attempts++ < 3) {
        frame = requestAnimationFrame(tryFocus);
      } else {
        pendingFocusRef.current = false;
      }
    };
    tryFocus();
    return () => cancelAnimationFrame(frame);
  }, [focusedRowId, shownRows]);

  const rowIndexFromEvent = (target: EventTarget | null): number => {
    const el = (target as HTMLElement | null)?.closest?.("[data-row-index]") as HTMLElement | null;
    if (el) return Number(el.dataset.rowIndex);
    if (activeRowId) {
      const i = shownRows.findIndex((r) => rowId(r) === activeRowId);
      if (i >= 0) return i;
    }
    return 0;
  };

  const onTreeKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (shownRows.length === 0) return;
    if (e.target instanceof HTMLInputElement) return;
    const idx = rowIndexFromEvent(e.target);
    const row = shownRows[idx];
    const last = shownRows.length - 1;
    const isErrorRow = (r: WaterfallRow) => r.type === "span" && r.node.span.isError;
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        focusRow(Math.min(idx + 1, last));
        return;
      case "ArrowUp":
        e.preventDefault();
        focusRow(Math.max(idx - 1, 0));
        return;
      case "Home":
        e.preventDefault();
        focusRow(0);
        return;
      case "End":
        e.preventDefault();
        focusRow(last);
        return;
      case "ArrowRight": {
        e.preventDefault();
        if (!row) return;
        if (row.type === "group") {
          if (collapsedGroups.has(row.group.key)) toggleGroup(row.group.key);
          else focusRow(Math.min(idx + 1, last));
        } else if (row.node.childCount > 0 && collapsedSpans.has(row.node.span.spanId)) {
          toggleSpanCollapse(row.node.span.spanId);
        } else if (row.node.childCount > 0) {
          focusRow(Math.min(idx + 1, last));
        }
        return;
      }
      case "ArrowLeft": {
        e.preventDefault();
        if (!row) return;
        if (row.type === "group") {
          if (!collapsedGroups.has(row.group.key)) toggleGroup(row.group.key);
          return;
        }
        if (row.node.childCount > 0 && !collapsedSpans.has(row.node.span.spanId)) {
          toggleSpanCollapse(row.node.span.spanId);
          return;
        }
        const parentId = row.node.parentId;
        const parentIdx = parentId
          ? shownRows.findIndex((r) => r.type === "span" && r.node.span.spanId === parentId)
          : shownRows.slice(0, idx).map((r, i) => (r.type === "group" ? i : -1)).filter((i) => i >= 0).pop() ?? -1;
        if (parentIdx >= 0) focusRow(parentIdx);
        return;
      }
      case "Enter":
      case " ":
        e.preventDefault();
        if (!row) return;
        if (row.type === "group") toggleGroup(row.group.key);
        else selectSpan(row.node.span.spanId);
        return;
      case "n":
      case "p": {
        if (e.ctrlKey || e.metaKey || e.altKey) return;
        e.preventDefault();
        const step = e.key === "n" ? 1 : -1;
        for (let i = idx + step; i >= 0 && i <= last; i += step) {
          const r = shownRows[i];
          if (isErrorRow(r) && r.type === "span") {
            focusRow(i);
            setSelectedId(r.node.span.spanId);
            return;
          }
        }
        return;
      }
      default:
    }
  };

  // --- wheel zoom / pan (native listener: React's wheel events are passive) --
  const timelineAreaRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = timelineAreaRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      const zoomIntent = e.ctrlKey || e.metaKey;
      const panIntent = e.shiftKey;
      if (!zoomIntent && !panIntent) return;
      const ruler = rulerRef.current;
      if (!ruler) return;
      const rect = ruler.getBoundingClientRect();
      if (rect.width <= 0) return;
      e.preventDefault();
      const cur = view;
      const full = fullView;
      const range = cur.endUs - cur.startUs;
      const fullRange = full.endUs - full.startUs;
      if (zoomIntent) {
        const frac = Math.min(Math.max((e.clientX - rect.left) / rect.width, 0), 1);
        const factor = Math.exp(e.deltaY * 0.002);
        const nextRange = Math.min(Math.max(range * factor, MIN_ZOOM_US), fullRange);
        if (nextRange >= fullRange) {
          setZoom(null);
          return;
        }
        const anchor = cur.startUs + frac * range;
        let start = anchor - frac * nextRange;
        start = Math.min(Math.max(start, full.startUs), full.endUs - nextRange);
        setZoom({ startUs: start, endUs: start + nextRange });
      } else {
        if (range >= fullRange) return;
        const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
        let start = cur.startUs + (delta / rect.width) * range;
        start = Math.min(Math.max(start, full.startUs), full.endUs - range);
        setZoom({ startUs: start, endUs: start + range });
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [view, fullView]);

  // --- ticks + gridlines ---------------------------------------------------
  const range = view.endUs - view.startUs;
  // Fewer, wider-spaced ticks when the timeline column is short, so labels
  // never overlap into an unreadable smear.
  const ticks = useMemo(() => computeTicks(range, tier === "wide" ? 8 : 4, view.startUs), [range, tier, view.startUs]);
  const stepPct = ticks.length > 1 ? ((ticks[1].offsetUs - ticks[0].offsetUs) / range) * 100 : 100;
  const firstTickPct = ticks.length > 0 ? (ticks[0].offsetUs / range) * 100 : 0;
  const gridStyle = useMemo<CSSProperties>(
    () => ({
      backgroundImage: GRIDLINE_BG,
      backgroundSize: `${stepPct}% 100%`,
      backgroundPosition: `${firstTickPct}% 0`,
    }),
    [stepPct, firstTickPct],
  );

  const renderRow = useCallback(
    (index: number, row: WaterfallRow) => {
      const id = rowId(row);
      const tabIndex: 0 | -1 = id === activeRowId ? 0 : -1;
      if (row.type === "group") {
        return (
          <GroupRow
            group={row.group}
            collapsed={collapsedGroups.has(row.group.key)}
            onToggle={toggleGroup}
            minStartUs={wf.minStartUs}
            view={view}
            gridStyle={gridStyle}
            gridClass={gridClass}
            showTimeline={showTimeline}
            a11y={{ id, index, level: 1, tabIndex }}
          />
        );
      }
      return (
        <SpanRow
          node={row.node}
          matched={row.matched}
          selected={selectedId === row.node.span.spanId}
          collapsed={collapsedSpans.has(row.node.span.spanId)}
          minStartUs={wf.minStartUs}
          view={view}
          gridStyle={gridStyle}
          gridClass={gridClass}
          showTimeline={showTimeline}
          tier={tier}
          a11y={{ id, index, level: row.node.depth + (grouped ? 2 : 1), tabIndex }}
          onSelect={selectSpan}
          onToggleCollapse={toggleSpanCollapse}
          onHover={handleHover}
        />
      );
    },
    [
      activeRowId,
      collapsedGroups,
      collapsedSpans,
      gridClass,
      gridStyle,
      grouped,
      handleHover,
      selectSpan,
      selectedId,
      showTimeline,
      tier,
      toggleGroup,
      toggleSpanCollapse,
      view,
      wf.minStartUs,
    ],
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

  const chipClass = (active: boolean) =>
    cn(
      "inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-3xs font-medium transition-colors",
      active ? "border-border bg-muted/60 text-foreground" : "border-transparent text-muted-foreground/60 hover:text-muted-foreground",
    );

  const kindChips = presentKinds.map((k) => {
    const active = kinds === null || kinds.has(k);
    const meta = KIND_META[k];
    return (
      <button key={k} type="button" onClick={() => toggleKind(k)} aria-pressed={active} className={chipClass(active)}>
        <span className={cn("size-1.5 rounded-full", meta.dot, !active && "opacity-40")} />
        {meta.label}
        <span className="font-mono tabular-nums">{kindCounts.get(k)}</span>
      </button>
    );
  });
  const hiddenKindCount = kinds === null ? 0 : presentKinds.length - kinds.size;

  const zoomHint = "Drag to zoom · ⌘/Ctrl+wheel zooms · Shift+wheel pans · double-click resets";

  return (
    <div
      ref={rootRef}
      className={cn(
        "flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border/60 bg-background",
        className,
      )}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          setSelectedId(null);
          setHoverTip(null);
        }
      }}
    >
      <SummaryHeader wf={wf} trace={trace} tier={tier} />

      {/* Toolbar: one row, scrolls horizontally when it must */}
      <div className="flex shrink-0 items-center gap-2 overflow-x-auto border-b border-border/50 px-3 py-1.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        <div className="relative min-w-[120px] flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2 size-3 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search spans…"
            aria-label="Search spans"
            className="h-6 w-full min-w-0 pl-6 text-xs"
          />
        </div>
        {tier === "wide" ? (
          <div className="flex shrink-0 items-center gap-1">{kindChips}</div>
        ) : (
          <Popover>
            <PopoverTrigger
              render={
                <button type="button" aria-label="Filter span kinds" className={chipClass(hiddenKindCount > 0)}>
                  Kinds
                  {hiddenKindCount > 0 && <span className="font-mono tabular-nums">−{hiddenKindCount}</span>}
                  <ChevronDown className="size-2.5" />
                </button>
              }
            />
            <PopoverContent align="start" className="flex w-auto max-w-64 flex-wrap gap-1 p-2">
              {kindChips}
            </PopoverContent>
          </Popover>
        )}
        <button
          type="button"
          onClick={() => setErrorsOnly((v) => !v)}
          aria-pressed={errorsOnly}
          className={cn(
            "inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-3xs font-medium transition-colors",
            errorsOnly
              ? "border-[color-mix(in_oklch,var(--tone-danger)_40%,transparent)] bg-[color-mix(in_oklch,var(--tone-danger)_10%,transparent)] text-[color:var(--tone-danger-fg)]"
              : "border-transparent text-muted-foreground/60 hover:text-muted-foreground",
          )}
        >
          <AlertTriangle className="size-2.5" />
          Errors
        </button>
        <span className="ml-auto flex shrink-0 items-center gap-2">
          {zoom && (
            <button
              type="button"
              onClick={() => setZoom(null)}
              className="inline-flex items-center gap-1 rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-3xs font-medium text-primary transition-colors hover:bg-primary/20"
            >
              <RotateCcw className="size-2.5" />
              Reset zoom
            </button>
          )}
          {wf.groups.length > 0 && (
            <label className="flex cursor-pointer items-center gap-1.5 text-3xs text-muted-foreground">
              <Switch checked={groupTurns} onCheckedChange={setGroupTurns} aria-label="Group turns" />
              Turns
            </label>
          )}
          <button
            type="button"
            onClick={expandAll}
            title="Expand all"
            aria-label="Expand all spans"
            className="rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <ChevronsUpDown className="size-3.5" />
          </button>
          <button
            type="button"
            onClick={collapseAll}
            title="Collapse all"
            aria-label="Collapse all spans"
            className="rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <ChevronsDownUp className="size-3.5" />
          </button>
        </span>
      </div>

      <div ref={timelineAreaRef} className="flex min-h-0 flex-1 flex-col">
        {/* Ruler (outside the scroller so it never competes with virtualised rows) */}
        <div className={cn(gridClass, "shrink-0 border-b border-border/60 bg-background/95")}>
          <div className="flex items-center justify-between py-1 pr-2 pl-2">
            <span className="text-3xs text-muted-foreground">{rows.length.toLocaleString()} rows</span>
            {tier === "wide" && (
              <span className="text-[9px] text-muted-foreground/60 italic" title={zoomHint}>
                drag to zoom
              </span>
            )}
          </div>
          <div
            ref={rulerRef}
            title={tier === "medium" ? zoomHint : undefined}
            className={cn(
              "relative h-7 cursor-crosshair touch-none border-l border-border/30 select-none",
              showTimeline ? "block" : "hidden",
            )}
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
            onPointerCancel={() => setBrush(null)}
            onPointerUp={() => {
              if (!brush) return;
              const lo = Math.min(brush.a, brush.b);
              const hi = Math.max(brush.a, brush.b);
              setBrush(null);
              if (hi - lo < 1) return; // click, not a drag
              const startUs = view.startUs + (lo / 100) * range;
              const endUs = view.startUs + (hi / 100) * range;
              if (endUs - startUs >= MIN_ZOOM_US) setZoom({ startUs, endUs });
            }}
          >
            {ticks
              .filter((t) => (t.offsetUs / range) * 100 <= 94) // keep the last label off the duration header
              .map((t) => (
                <span
                  key={t.offsetUs}
                  className="absolute top-1/2 -translate-y-1/2 pl-1 font-mono text-[9px] text-muted-foreground tabular-nums"
                  style={{ left: `${(t.offsetUs / range) * 100}%` }}
                >
                  {t.label}
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
          <div className="py-1 pr-3 text-right text-[9px] text-muted-foreground/60">
            {tier === "wide" ? "duration" : ""}
          </div>
        </div>

        {/* Minimap */}
        {showTimeline && (
          <div className={cn(gridClass, "shrink-0 border-b border-border/40")}>
            <div />
            <div className="border-l border-border/30">
              <Minimap wf={wf} fullView={fullView} view={view} onChangeView={setZoom} />
            </div>
            <div />
          </div>
        )}

        {/* Rows */}
        {shownRows.length === 0 ? (
          <p className="px-3 py-8 text-center text-2xs text-muted-foreground">No spans match the current filters.</p>
        ) : (
          <div
            role="tree"
            aria-label="Trace spans"
            className="flex min-h-0 flex-1 flex-col"
            onKeyDown={onTreeKeyDown}
            onFocus={(e) => {
              const el = (e.target as HTMLElement).closest?.("[data-row-id]") as HTMLElement | null;
              if (el?.dataset.rowId && el.dataset.rowId !== focusedRowId) setFocusedRowId(el.dataset.rowId);
            }}
          >
            <Virtuoso<WaterfallRow>
              ref={virtuosoRef}
              className="min-h-0 flex-1"
              data={shownRows}
              fixedItemHeight={ROW_HEIGHT}
              increaseViewportBy={ROW_HEIGHT * 8}
              computeItemKey={(_, row) => rowId(row)}
              itemContent={renderRow}
            />
          </div>
        )}
        {capped && (
          <p className="shrink-0 border-t border-border/40 px-3 py-1.5 text-center text-2xs text-muted-foreground">
            {trace.isComplete
              ? `Showing the first ${RENDER_CAP.toLocaleString()} of ${rows.length.toLocaleString()} rows`
              : `Live · showing the newest ${RENDER_CAP.toLocaleString()} of ${rows.length.toLocaleString()} rows`}
            {" — narrow with search or filters."}
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
