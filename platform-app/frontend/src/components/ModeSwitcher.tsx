import { useState, useRef, useEffect, useCallback } from "react";
import { createPortal } from "react-dom";
import { client } from "@/lib/client";
import { useAvailableModes } from "@/hooks/useAvailableModes";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ChevronDown, Check, Loader2 } from "lucide-react";
import { toneSoft, toneText } from "@/lib/status";

export function ModeSwitcher({
  namespace,
  runName,
  currentMode,
  onSwitched,
  segment = false,
}: {
  namespace: string;
  runName: string;
  currentMode: string;
  onSwitched?: () => void;
  /**
   * Render as a bare statusline segment (inherits the surrounding mono
   * type and tone color) instead of a standalone outline button.
   */
  segment?: boolean;
}) {
  const { modes, loading } = useAvailableModes(namespace);
  const [open, setOpen] = useState(false);
  const [switching, setSwitching] = useState(false);
  const [result, setResult] = useState<{
    type: "applied" | "denied" | "noop";
    message: string;
  } | null>(null);
  const btnRef = useRef<HTMLButtonElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ top: 0, left: 0 });

  // Position the portal dropdown below the button.
  const updatePos = useCallback(() => {
    if (!btnRef.current) return;
    const r = btnRef.current.getBoundingClientRect();
    setPos({ top: r.bottom + 4, left: r.left });
  }, []);

  useEffect(() => {
    if (!open) return;
    updatePos();
    window.addEventListener("scroll", updatePos, true);
    window.addEventListener("resize", updatePos);
    return () => {
      window.removeEventListener("scroll", updatePos, true);
      window.removeEventListener("resize", updatePos);
    };
  }, [open, updatePos]);

  // Close on outside click.
  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      const t = e.target as Node;
      if (btnRef.current?.contains(t)) return;
      if (dropdownRef.current?.contains(t)) return;
      setOpen(false);
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  // Close on Escape and return focus to the trigger.
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        btnRef.current?.focus();
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [open]);

  useEffect(() => {
    if (result) {
      const t = setTimeout(() => setResult(null), 3000);
      return () => clearTimeout(t);
    }
  }, [result]);

  const handleSwitch = async (targetMode: string) => {
    if (switching) return;
    setSwitching(true);
    setResult(null);
    try {
      const resp = await client.switchAgentRunMode({
        namespace,
        name: runName,
        targetMode,
        source: "ui",
      });
      switch (resp.result) {
        case "applied":
          setResult({
            type: "applied",
            message: `Switched to ${resp.newMode}`,
          });
          onSwitched?.();
          break;
        case "denied":
          setResult({
            type: "denied",
            message: resp.denialReason || "Switch denied",
          });
          break;
        case "noop":
          setResult({ type: "noop", message: `Already in ${currentMode}` });
          break;
      }
    } catch (err) {
      setResult({
        type: "denied",
        message: err instanceof Error ? err.message : "Switch failed",
      });
    } finally {
      setSwitching(false);
      setOpen(false);
    }
  };

  return (
    <>
      {segment ? (
        <button
          ref={btnRef}
          type="button"
          onClick={() => setOpen(!open)}
          disabled={loading || switching}
          title="Switch mode"
          className="group inline-flex items-center gap-1 rounded-sm outline-none transition-opacity hover:opacity-75 focus-visible:ring-2 focus-visible:ring-ring/60 disabled:opacity-50"
        >
          {currentMode || "mode"}
          {switching ? (
            <Loader2 className="size-3 animate-spin" />
          ) : (
            <ChevronDown className="size-3 opacity-50 transition-opacity group-hover:opacity-100" />
          )}
        </button>
      ) : (
        <Button
          ref={btnRef}
          variant="outline"
          size="sm"
          onClick={() => setOpen(!open)}
          disabled={loading || switching}
          className="gap-1.5"
        >
          {switching ? (
            <Loader2 className="h-3 w-3 animate-spin" />
          ) : (
            <ChevronDown className="h-3 w-3" />
          )}
          Mode
        </Button>
      )}

      {result &&
        createPortal(
          <div
            style={{ position: "fixed", top: pos.top, left: pos.left, zIndex: 9999 }}
            className={`rounded-md px-3 py-1.5 text-xs whitespace-nowrap shadow-md ${
              result.type === "applied"
                ? toneSoft.success
                : result.type === "denied"
                  ? toneSoft.danger
                  : toneSoft.warning
            }`}
          >
            {result.message}
          </div>,
          document.body,
        )}

      {open &&
        createPortal(
          <div
            ref={dropdownRef}
            role="menu"
            aria-label="Switch mode"
            style={{ position: "fixed", top: pos.top, left: pos.left, zIndex: 9999 }}
            className="w-64 rounded-md border bg-popover p-1 shadow-md"
          >
            <div className="px-2 py-1.5 text-xs font-medium text-muted-foreground">
              Switch Mode
            </div>
            <div className="max-h-64 overflow-y-auto">
              {modes.map((m) => {
                const key = m.k8sName || m.name;
                const isCurrent = key === currentMode;
                return (
                  <button
                    key={key}
                    type="button"
                    role="menuitem"
                    onClick={() => handleSwitch(key)}
                    disabled={isCurrent}
                    className={`flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-accent focus-visible:outline-none focus-visible:bg-accent focus-visible:ring-2 focus-visible:ring-ring/60 ${
                      isCurrent ? "opacity-50" : ""
                    }`}
                  >
                    <span className="flex min-w-0 flex-1 flex-col text-left">
                      <span className="truncate">{key}</span>
                      <span className="truncate text-[11px] text-muted-foreground">
                        {m.description || m.category || "Mode"}
                      </span>
                    </span>
                    {m.category && (
                      <Badge variant="secondary" className="text-[10px] px-1.5">
                        {m.category}
                      </Badge>
                    )}
                    {isCurrent && <Check className={`h-3 w-3 ${toneText.success}`} />}
                  </button>
                );
              })}
            </div>
          </div>,
          document.body,
        )}
    </>
  );
}
