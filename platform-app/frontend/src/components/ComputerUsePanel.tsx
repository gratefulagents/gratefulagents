import { useEffect, useId, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  Accessibility, AlertTriangle, Ban, Camera, Check, Crosshair, Globe, Keyboard, Laptop, Monitor, MousePointer2,
  MousePointerClick, Move, MoveVertical, Pause, Play, RotateCcw, ScreenShare, Square, Type, X, ZoomIn,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { LiveDot, type LiveDotTone } from "@/components/ui/live-dot";
import { Spinner } from "@/components/ui/spinner";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { useNow } from "@/hooks/useNow";
import {
  decideComputerUse, pauseComputerUse, resumeComputerUse, setSessionApprovalMode, startComputerUse, stopComputerUse,
  useComputerUse, UNAVAILABLE_MESSAGE, type ComputerUseState, type TimelineEntry,
} from "@/lib/computer-use/controller";
import { actionMarker, describeAction, type Marker } from "@/lib/computer-use/describe";
import type { ComputerAction, Display } from "@/lib/computer-use/native";
import type { ApprovalMode } from "@/lib/computer-use/preferences";
import { usePermissionGrant, useNativeStatus } from "@/lib/computer-use/use-native-status";
import { formatPollTime } from "@/lib/format";
import { toneSoft, toneText } from "@/lib/status";
import { cn } from "@/lib/utils";

export interface ComputerUseView {
  panel: HTMLElement | null;
  shortcut: HTMLElement | null;
  open: () => void;
}

const ACTION_ICONS: Record<ComputerAction["action"], typeof Monitor> = {
  screenshot: Camera, left_click: MousePointerClick, right_click: MousePointerClick, middle_click: MousePointerClick,
  double_click: MousePointerClick, triple_click: MousePointerClick, mouse_move: MousePointer2, left_click_drag: Move,
  left_mouse_down: MousePointerClick, left_mouse_up: MousePointerClick, scroll: MoveVertical, type: Type, key: Keyboard,
  wait: Crosshair, cursor_position: Crosshair, zoom: ZoomIn, open_url: Globe,
};

const isLive = (state: ComputerUseState) => state.phase === "active" || state.phase === "paused" || state.phase === "stopping";

function phaseLabel(state: ComputerUseState): { label: string; tone: LiveDotTone; pulse: boolean } {
  if (state.phase === "stopping") return { label: "Stopping…", tone: "idle", pulse: false };
  if (state.pending?.needsApproval) return { label: "Needs approval", tone: "waiting", pulse: true };
  if (state.phase === "paused") return { label: "Paused", tone: "waiting", pulse: false };
  return { label: "Agent in control", tone: "running", pulse: true };
}

/**
 * Computer use for one run. Renders into the inspector's Computer tab and the
 * chat shortcut slot through portals; the session itself lives in the
 * module-level controller, so unmounting this never interrupts it.
 */
export function ComputerUsePanel({ namespace, name, enabled, view }: {
  namespace: string;
  name: string;
  enabled: boolean;
  view: ComputerUseView;
}) {
  const state = useComputerUse();
  const mine = state.run?.namespace === namespace && state.run?.name === name;
  const live = mine && isLive(state);
  return (
    <>
      {view.panel && createPortal(
        <div className="mx-auto flex w-full max-w-[960px] flex-col gap-3 p-3">
          {live
            ? <ActiveView state={state} panel={view.panel} />
            : <SetupView state={state} mine={mine} enabled={enabled} panel={view.panel} run={{ namespace, name }} />}
        </div>,
        view.panel,
      )}
      {view.shortcut && live && createPortal(<ShortcutPill state={state} onOpen={view.open} />, view.shortcut)}
    </>
  );
}

const panelVisible = (panel: HTMLElement | null) => () => !panel?.closest("[hidden]");

const SEGMENT = "text-muted-foreground aria-pressed:bg-primary/12 aria-pressed:text-foreground aria-pressed:font-semibold";

export function ApprovalModeToggle({ value, onChange, className }: {
  value: ApprovalMode;
  onChange: (mode: ApprovalMode) => void;
  className?: string;
}) {
  return (
    <ToggleGroup
      aria-label="Approval mode"
      value={[value]}
      onValueChange={(next) => {
        const mode = next[0] as ApprovalMode | undefined;
        if (mode) onChange(mode);
      }}
      variant="outline"
      size="sm"
      spacing={0}
      className={className}
    >
      <ToggleGroupItem value="auto" className={SEGMENT}>Autonomous</ToggleGroupItem>
      <ToggleGroupItem value="ask" className={SEGMENT}>Ask first</ToggleGroupItem>
    </ToggleGroup>
  );
}

export function PermissionRow({ icon, title, detail, granted, onGrant, children }: {
  icon: ReactNode;
  title: string;
  detail: string;
  granted: boolean;
  onGrant: () => void;
  children?: ReactNode;
}) {
  return (
    <li className="flex items-start gap-3 py-2.5">
      <span className={cn(
        "mt-0.5 grid size-7 shrink-0 place-items-center rounded-full [&_svg]:size-3.5",
        granted ? toneSoft.success : "bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/70",
      )}>
        {granted ? <Check /> : icon}
      </span>
      <div className="min-w-0 flex-1">
        <p className="text-[13px] font-medium leading-6">{title}</p>
        <p className="text-[12px] leading-snug text-muted-foreground">{detail}</p>
        {children}
      </div>
      {granted ? (
        <span className="mt-1 inline-flex items-center gap-1.5 text-[12px] text-muted-foreground">
          <LiveDot tone="success" size="xs" />Granted
        </span>
      ) : (
        <Button size="sm" variant="outline" className="mt-0.5" onClick={onGrant} aria-label={`Grant ${title}`}>
          <LiveDot tone="waiting" size="xs" />Grant
        </Button>
      )}
    </li>
  );
}

export function RelaunchHint({ onRelaunch }: { onRelaunch: () => void }) {
  return (
    <div className="mt-2 flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">
      <span>macOS applies Screen Recording after the app restarts.</span>
      <Button size="xs" variant="secondary" onClick={onRelaunch}><RotateCcw />Relaunch app</Button>
    </div>
  );
}

function Notice({ tone, icon, children }: { tone: "warning" | "danger" | "info"; icon: ReactNode; children: ReactNode }) {
  return (
    <div role={tone === "info" ? "note" : "alert"} className={cn("flex items-start gap-2 rounded-lg px-3 py-2 text-[12px] leading-snug [&_svg]:mt-px [&_svg]:size-3.5 [&_svg]:shrink-0", toneSoft[tone])}>
      {icon}<div className="min-w-0">{children}</div>
    </div>
  );
}

function SetupView({ state, mine, enabled, panel, run }: {
  state: ComputerUseState;
  mine: boolean;
  enabled: boolean;
  panel: HTMLElement;
  run: { namespace: string; name: string };
}) {
  const { status, error: statusError, refresh } = useNativeStatus(panelVisible(panel));
  const { grant, relaunch, requestedScreen, error: grantError } = usePermissionGrant(refresh);
  const [chosen, setChosen] = useState<number | undefined>();
  const displays = status?.displays ?? [];
  const displayId = displays.some((display) => display.id === chosen)
    ? chosen
    : (displays.find((display) => display.primary) ?? displays[0])?.id;
  const starting = mine && state.phase === "starting";
  const elsewhere = !mine && state.run && state.phase !== "idle" && state.phase !== "error" ? state.run : undefined;
  const ready = !!status?.supported && status.accessibility && status.screenRecording && displayId !== undefined;

  if (!status) {
    return (
      <div className="surface-card flex items-center gap-2 p-4 text-[12.5px] text-muted-foreground" role="status">
        {statusError ? <><AlertTriangle className="size-4" />Could not check this Mac: {statusError}</> : <><Spinner />Checking this Mac…</>}
      </div>
    );
  }

  if (!status.supported) {
    return (
      <section className="surface-card flex items-start gap-3 p-4">
        <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-muted/60 text-muted-foreground"><Laptop className="size-4.5" /></span>
        <div>
          <h2 className="text-[14px] font-semibold tracking-[-0.01em]">Computer use runs in the Mac app</h2>
          <p className="mt-1 text-[12.5px] leading-relaxed text-muted-foreground">
            Open this run in the Grateful Agents desktop app on macOS to let the agent see a display and use the mouse and keyboard.
          </p>
        </div>
      </section>
    );
  }

  return (
    <section className="surface-card overflow-hidden" aria-labelledby="computer-use-setup-title">
      <header className="flex items-start gap-3 px-4 pt-4">
        <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-primary/10 text-primary"><Monitor className="size-4.5" /></span>
        <div className="min-w-0">
          <h2 id="computer-use-setup-title" className="text-[14px] font-semibold leading-6 tracking-[-0.01em]">Let the agent use this Mac</h2>
          <p className="text-[12.5px] leading-snug text-muted-foreground">You watch every step live and can pause or stop at any time.</p>
        </div>
      </header>

      <div className="space-y-2 px-4 pt-3">
        {mine && state.phase === "error" && (
          state.available
            ? <Notice tone="danger" icon={<AlertTriangle />}>{state.error}</Notice>
            : <Notice tone="warning" icon={<Ban />}>{UNAVAILABLE_MESSAGE}</Notice>
        )}
        {mine && state.phase === "idle" && state.stoppedReason && (
          <Notice tone="info" icon={<Square />}>Session ended: {state.stoppedReason}</Notice>
        )}
        {elsewhere && (
          <Notice tone="info" icon={<ScreenShare />}>
            Another run (<span className="font-mono">{elsewhere.name}</span>) is using this Mac. Starting here stops it.
          </Notice>
        )}
        {!enabled && <Notice tone="info" icon={<Ban />}>Only the run owner can start computer use, and only while the run is active.</Notice>}
        {(grantError || statusError) && <Notice tone="danger" icon={<AlertTriangle />}>{grantError || statusError}</Notice>}
      </div>

      <ol className="mx-4 mt-1 divide-y divide-border/60">
        <PermissionRow icon={<Accessibility />} title="Accessibility" detail="Lets the agent click, scroll and type."
          granted={status.accessibility} onGrant={() => void grant("accessibility")} />
        <PermissionRow icon={<ScreenShare />} title="Screen Recording" detail="Lets the agent see the display you choose."
          granted={status.screenRecording} onGrant={() => void grant("screen_recording")}>
          {requestedScreen && !status.screenRecording && <RelaunchHint onRelaunch={() => void relaunch()} />}
        </PermissionRow>
        <li className="flex items-start gap-3 py-2.5">
          <span className={cn(
            "mt-0.5 grid size-7 shrink-0 place-items-center rounded-full [&_svg]:size-3.5",
            displayId !== undefined ? toneSoft.success : "bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/70",
          )}>
            {displayId !== undefined ? <Check /> : <Monitor />}
          </span>
          <div className="min-w-0 flex-1">
            <p className="text-[13px] font-medium leading-6">Display</p>
            <p className="text-[12px] leading-snug text-muted-foreground">The agent sees and controls only this display.</p>
            <DisplayPicker displays={displays} value={displayId} onChange={setChosen} />
          </div>
        </li>
      </ol>

      <footer className="mt-1 flex flex-col gap-2.5 border-t bg-muted/25 px-4 py-3 sm:flex-row sm:items-center">
        <Button size="lg" className="px-4" disabled={!ready || !enabled || starting}
          onClick={() => void startComputerUse(run, displayId!)}>
          {starting ? <Spinner /> : <Play />}{starting ? "Starting…" : "Start"}
        </Button>
        <p className="text-[11.5px] leading-snug text-muted-foreground">
          The agent will see the selected display and can control the mouse and keyboard.
          Stop anytime with <Kbd className="mx-0.5 tracking-wider">⌃⌥⌘⎋</Kbd>.
        </p>
      </footer>
    </section>
  );
}

function DisplayPicker({ displays, value, onChange }: { displays: Display[]; value?: number; onChange: (id: number) => void }) {
  if (!displays.length) return <p className="mt-2 text-[12px] text-muted-foreground">No displays found.</p>;
  return (
    <div role="radiogroup" aria-label="Display" className="mt-2 grid gap-1.5">
      {displays.map((display) => {
        const selected = display.id === value;
        return (
          <button key={display.id} type="button" role="radio" aria-checked={selected} onClick={() => onChange(display.id)}
            className={cn(
              "flex items-center gap-2.5 rounded-lg border px-2.5 py-2 text-left transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50",
              selected ? "border-primary/60 bg-primary/5" : "border-border hover:bg-muted/60",
            )}>
            <span className={cn("grid size-4 shrink-0 place-items-center rounded-full border", selected ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground/40")}>
              {selected && <span className="size-1.5 rounded-full bg-current" />}
            </span>
            <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium">{display.name}</span>
            {display.primary && <span className="rounded-full bg-muted px-1.5 text-[10.5px] text-muted-foreground">Primary</span>}
            <span className="font-mono text-[11px] tabular-nums text-muted-foreground">{display.width}×{display.height}</span>
          </button>
        );
      })}
    </div>
  );
}

function ActiveView({ state, panel }: { state: ComputerUseState; panel: HTMLElement }) {
  const { status } = useNativeStatus(() => false);
  const display = status?.displays.find((candidate) => candidate.id === state.displayId);
  const phase = phaseLabel(state);
  const paused = state.phase === "paused";
  const stopping = state.phase === "stopping";
  return (
    <>
      <header className="surface-card flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
        <div role="status" aria-label={`Computer use: ${phase.label}`}
          className={cn("inline-flex h-7 items-center gap-2 rounded-full px-2.5 text-[12px] font-medium",
            phase.tone === "running" ? toneSoft.running : phase.tone === "waiting" ? toneSoft.warning : toneSoft.neutral)}>
          <LiveDot tone={phase.tone} pulse={phase.pulse} />{phase.label}
        </div>
        <div className="flex min-w-0 flex-1 items-center gap-1.5 text-[12px] text-muted-foreground">
          <Monitor className="size-3.5 shrink-0" />
          <span className="truncate">{display?.name ?? `Display ${state.displayId ?? ""}`}</span>
        </div>
        <div className="flex items-center gap-1.5">
          <Button size="sm" variant="outline" disabled={stopping}
            onClick={() => void (paused ? resumeComputerUse() : pauseComputerUse())}>
            {paused ? <Play /> : <Pause />}{paused ? "Resume" : "Pause"}
          </Button>
          <Button size="sm" disabled={stopping} onClick={() => void stopComputerUse()}
            className="bg-[color:var(--tone-danger)] text-white hover:bg-[color:var(--tone-danger)]/90">
            <Square className="fill-current" />Stop
          </Button>
        </div>
      </header>

      {state.error && <Notice tone="danger" icon={<AlertTriangle />}>{state.error}</Notice>}

      <LiveView state={state} panel={panel} />

      <div className="flex flex-wrap items-center justify-between gap-2 px-0.5">
        <div>
          <p className="text-[12px] font-medium">Approval</p>
          <p className="text-[11.5px] text-muted-foreground">
            {state.approvalMode === "ask" ? "You allow each click, keystroke and URL." : "Actions run as soon as the agent asks."}
          </p>
        </div>
        <ApprovalModeToggle value={state.approvalMode} onChange={setSessionApprovalMode} />
      </div>

      <Timeline entries={state.timeline} />
    </>
  );
}

function LiveView({ state, panel }: { state: ComputerUseState; panel: HTMLElement }) {
  const { latest, pending, current } = state;
  const live = pending?.request.action ?? current?.action;
  // Without a live action, mark the most recent pointer action: that is where typing lands.
  const marker = latest
    ? live
      ? actionMarker(live, latest, latest.cursor)
      : state.timeline.filter((entry) => entry.status !== "denied")
        .map((entry) => actionMarker(entry.action, latest, latest.cursor)).find(Boolean) ?? null
    : null;
  const waiting = !pending && !current && state.phase === "active";
  return (
    <figure className="relative overflow-hidden rounded-xl border bg-[oklch(0.18_0_0)] shadow-sm"
      style={{ aspectRatio: latest ? `${latest.width} / ${latest.height}` : "16 / 10" }}>
      {latest ? (
        <img src={latest.dataUrl} alt="Latest screenshot of the controlled display" className="absolute inset-0 size-full object-contain" />
      ) : (
        <div className="absolute inset-0 grid place-items-center text-[12px] text-white/50">
          <span className="flex flex-col items-center gap-2"><Monitor className="size-6" />The first screenshot appears here</span>
        </div>
      )}
      {latest && marker && <MarkerOverlay marker={marker} width={latest.width} height={latest.height} live={!!live} />}
      {waiting && (
        <div className="absolute left-1/2 top-2.5 inline-flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-black/55 px-2.5 py-1 text-[11.5px] text-white/85 backdrop-blur-sm">
          <Spinner className="size-3" />Waiting for the agent…
        </div>
      )}
      {current && (
        <div className="absolute left-1/2 top-2.5 inline-flex max-w-[90%] -translate-x-1/2 items-center gap-1.5 rounded-full bg-black/55 px-2.5 py-1 text-[11.5px] text-white/90 backdrop-blur-sm">
          <Spinner className="size-3" /><span className="truncate">{describeAction(current.action)}</span>
        </div>
      )}
      {pending?.needsApproval && (
        <ApprovalCard action={pending.request.action} panel={panel}
          top={!!latest && markerY(marker) > latest.height / 2} />
      )}
      {pending && !pending.needsApproval && state.phase === "paused" && (
        <div className="absolute inset-x-2.5 bottom-2.5 flex items-center gap-2 rounded-lg bg-black/60 px-3 py-2 text-[12px] text-white/90 backdrop-blur-sm">
          <Pause className="size-3.5 shrink-0" /><span className="truncate">Paused. Next: {describeAction(pending.request.action)}</span>
        </div>
      )}
    </figure>
  );
}

const markerY = (marker: Marker | null) =>
  !marker ? 0 : marker.kind === "drag" ? Math.max(marker.y1, marker.y2) : marker.kind === "rect" ? marker.y + marker.height / 2 : marker.y;

function MarkerOverlay({ marker, width, height, live }: { marker: Marker; width: number; height: number; live: boolean }) {
  const arrowId = `cu-arrow-${useId().replace(/[^\w-]/g, "")}`;
  const unit = Math.max(width, height) / 100;
  const color = live ? "var(--tone-info)" : "var(--tone-warning)";
  const stroke = { stroke: color, strokeWidth: 2.5, vectorEffect: "non-scaling-stroke" as const, fill: "none" };
  const halo = { stroke: "white", strokeOpacity: 0.85, strokeWidth: 5, vectorEffect: "non-scaling-stroke" as const, fill: "none" };
  const arrow = (x1: number, y1: number, x2: number, y2: number) => (
    <>
      <line x1={x1} y1={y1} x2={x2} y2={y2} {...halo} />
      <line x1={x1} y1={y1} x2={x2} y2={y2} {...stroke} markerEnd={`url(#${arrowId})`} />
    </>
  );
  let shape: ReactNode;
  switch (marker.kind) {
    case "point":
      shape = (
        <g>
          <circle cx={marker.x} cy={marker.y} r={2.2 * unit} {...halo} />
          <circle cx={marker.x} cy={marker.y} r={2.2 * unit} {...stroke} fill={color} fillOpacity={0.18} />
          <circle cx={marker.x} cy={marker.y} r={0.35 * unit} fill={color} stroke="white" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          {live && <circle cx={marker.x} cy={marker.y} r={2.2 * unit} {...stroke} className="origin-center animate-ping [transform-box:fill-box]" />}
        </g>
      );
      break;
    case "drag":
      shape = (
        <g>
          <circle cx={marker.x1} cy={marker.y1} r={0.8 * unit} fill={color} stroke="white" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          {arrow(marker.x1, marker.y1, marker.x2, marker.y2)}
        </g>
      );
      break;
    case "scroll":
      shape = (
        <g>
          <circle cx={marker.x} cy={marker.y} r={0.8 * unit} fill={color} stroke="white" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          {arrow(marker.x, marker.y, marker.x + marker.dx * 7 * unit, marker.y + marker.dy * 7 * unit)}
        </g>
      );
      break;
    case "rect":
      shape = (
        <g>
          <rect x={marker.x} y={marker.y} width={marker.width} height={marker.height} {...halo} />
          <rect x={marker.x} y={marker.y} width={marker.width} height={marker.height} {...stroke} fill={color} fillOpacity={0.12} />
        </g>
      );
      break;
  }
  return (
    <svg className="pointer-events-none absolute inset-0 size-full" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="xMidYMid meet"
      data-testid="action-marker" data-kind={marker.kind} aria-hidden>
      <defs>
        <marker id={arrowId} viewBox="0 0 10 10" refX="7" refY="5" markerWidth="5" markerHeight="5" orient="auto-start-reverse">
          <path d="M0 0 L10 5 L0 10 z" fill={color} />
        </marker>
      </defs>
      {shape}
    </svg>
  );
}

const isEditable = (target: EventTarget | null) =>
  target instanceof HTMLElement && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName));

/** `top` moves the card out of the way when the target sits in the lower half of the frame. */
function ApprovalCard({ action, panel, top }: { action: ComputerAction; panel: HTMLElement; top: boolean }) {
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Enter" || event.defaultPrevented || isEditable(event.target) || panel.closest("[hidden]")) return;
      event.preventDefault();
      decideComputerUse(true);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [panel]);
  const Icon = ACTION_ICONS[action.action];
  const detail = action.action === "type" ? action.text : action.action === "open_url" ? action.url : undefined;
  return (
    <div role="alertdialog" aria-label="Approve the agent's next action"
      className={cn("absolute inset-x-2.5 rounded-xl border bg-background/95 p-3 shadow-lg backdrop-blur-md", top ? "top-2.5" : "bottom-2.5")}>
      <div className="flex items-start gap-2.5">
        <span className={cn("grid size-8 shrink-0 place-items-center rounded-lg [&_svg]:size-4", toneSoft.warning)}><Icon /></span>
        <div className="min-w-0 flex-1">
          <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">The agent wants to</p>
          <p className="truncate text-[13.5px] font-medium">{describeAction(action)}</p>
          {detail && (
            <pre className="mt-1.5 max-h-24 overflow-auto rounded-md bg-muted/70 px-2 py-1.5 font-mono text-[11.5px] whitespace-pre-wrap break-all">{detail}</pre>
          )}
        </div>
      </div>
      <div className="mt-3 flex justify-end gap-2">
        <Button size="sm" variant="outline" onClick={() => decideComputerUse(false)}><X />Deny</Button>
        <Button size="sm" onClick={() => decideComputerUse(true)} aria-keyshortcuts="Enter">
          <Check />Allow<Kbd className="ml-0.5 h-4 bg-primary-foreground/15 text-primary-foreground">↩</Kbd>
        </Button>
      </div>
    </div>
  );
}

const STATUS_META: Record<TimelineEntry["status"], { label: string; className: string }> = {
  running: { label: "Running", className: toneSoft.running },
  done: { label: "Done", className: "bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/70" },
  failed: { label: "Failed", className: toneSoft.danger },
  denied: { label: "Denied", className: toneSoft.neutral },
};

function Timeline({ entries }: { entries: TimelineEntry[] }) {
  const now = useNow(10_000);
  return (
    <section aria-label="Recent actions" className="surface-card px-3 py-2">
      <h3 className="py-1 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">Recent actions</h3>
      {entries.length === 0 ? (
        <p className="py-3 text-[12px] text-muted-foreground">Nothing yet. Actions appear here as the agent works.</p>
      ) : (
        <ol className="divide-y divide-border/60">
          {entries.map((entry) => {
            const Icon = ACTION_ICONS[entry.action.action];
            const meta = STATUS_META[entry.status];
            return (
              <li key={entry.id} className="flex items-center gap-2.5 py-2">
                <span className={cn("grid size-7 shrink-0 place-items-center rounded-lg [&_svg]:size-3.5", meta.className)}>
                  {entry.status === "running" ? <Spinner className="size-3.5" /> : <Icon />}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[12.5px] font-medium">{describeAction(entry.action)}</p>
                  <p className="text-[11px] text-muted-foreground">
                    <span className={cn(entry.status === "failed" && toneText.danger)}>{meta.label}</span>
                    {" · "}{now - entry.startedAt < 10_000
                      ? "just now"
                      : formatPollTime(BigInt(Math.floor(entry.startedAt / 1000)), now)}
                  </p>
                  {entry.error && entry.status !== "running" && (
                    <p className={cn("mt-0.5 line-clamp-2 text-[11.5px]", entry.status === "failed" ? toneText.danger : "text-muted-foreground")}>{entry.error}</p>
                  )}
                </div>
                {entry.thumbnail && (
                  <img src={entry.thumbnail} alt="" className="h-10 w-16 shrink-0 rounded-md border object-cover" />
                )}
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}

function ShortcutPill({ state, onOpen }: { state: ComputerUseState; onOpen: () => void }) {
  const phase = phaseLabel(state);
  const needsApproval = !!state.pending?.needsApproval;
  return (
    <div className="border-t px-3 py-2 md:px-4">
      <div role="status" aria-label={`Computer: ${phase.label}`}
        className={cn("flex items-center gap-2 rounded-full py-1 pr-1 pl-3 text-[12px]",
          needsApproval ? toneSoft.warning : "bg-muted/50 ring-1 ring-inset ring-border/70")}>
        <LiveDot tone={phase.tone} pulse={phase.pulse} />
        <span className="font-medium">Computer</span>
        <span className="text-muted-foreground">·</span>
        <span className="min-w-0 flex-1 truncate">{phase.label}</span>
        <Button size="xs" variant={needsApproval ? "default" : "ghost"} onClick={onOpen}>Open</Button>
        <Button size="xs" variant="ghost" className={toneText.danger} aria-label="Stop computer use" onClick={() => void stopComputerUse()}>
          <Square className="fill-current" />Stop
        </Button>
      </div>
    </div>
  );
}

