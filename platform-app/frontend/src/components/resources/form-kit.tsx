import * as React from "react";
import { Plus, X } from "lucide-react";

import { cn } from "@/lib/utils";
import { resourceNameError } from "@/lib/resourceNames";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { FlowField } from "@/components/create-flow/create-flow";
import type { KeyValueRow } from "@/components/resources/resource-helpers";
import { keyValueRowsError } from "@/components/resources/resource-helpers";

/* ── Section ──────────────────────────────────────────────────── */

/** Eyebrow-titled group of fields inside a resource editor. */
export function FormSection({
  title,
  description,
  aside,
  children,
  className,
}: {
  title: string;
  description?: React.ReactNode;
  aside?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("space-y-3.5", className)}>
      <div className="flex items-baseline justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground/80">{title}</h3>
          {description && <p className="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">{description}</p>}
        </div>
        {aside}
      </div>
      <div className="space-y-4">{children}</div>
    </section>
  );
}

/** Two-column grid that collapses on phones. */
export function FieldGrid({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("grid gap-4 sm:grid-cols-2", className)}>{children}</div>;
}

/* ── Messages ─────────────────────────────────────────────────── */

export function FieldError({ id, children }: { id?: string; children: React.ReactNode }) {
  if (!children) return null;
  return (
    <p id={id} role="alert" className="text-[11.5px] leading-snug text-destructive">
      {children}
    </p>
  );
}

/** Soft callout for consequences the user should read before saving. */
export function InlineNote({
  tone = "info",
  children,
  className,
}: {
  tone?: "info" | "warning" | "danger";
  children: React.ReactNode;
  className?: string;
}) {
  const tones = {
    info: "border-border/70 bg-muted/40 text-muted-foreground",
    warning: "border-[color-mix(in_oklch,var(--tone-warning)_35%,transparent)] bg-[color-mix(in_oklch,var(--tone-warning)_10%,transparent)] text-[color:var(--tone-warning-fg)]",
    danger: "border-[color-mix(in_oklch,var(--tone-danger)_35%,transparent)] bg-[color-mix(in_oklch,var(--tone-danger)_10%,transparent)] text-[color:var(--tone-danger-fg)]",
  } as const;
  return (
    <div role={tone === "info" ? undefined : "note"} className={cn("rounded-lg border px-3 py-2 text-[11.5px] leading-relaxed", tones[tone], className)}>
      {children}
    </div>
  );
}

/* ── Name ─────────────────────────────────────────────────────── */

/**
 * Kubernetes-style resource name. Validates as the user types and locks on
 * edit, since names are the identity every reference points at.
 */
export function NameField({
  id,
  value,
  onChange,
  isNew,
  placeholder = "my-resource",
  hint,
  autoFocus,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  isNew: boolean;
  placeholder?: string;
  hint?: React.ReactNode;
  autoFocus?: boolean;
}) {
  const error = isNew ? resourceNameError(value.trim()) : null;
  return (
    <FlowField
      id={id}
      label="Name"
      required={isNew}
      hint={!isNew ? "Names can't change after creation; references point at them." : (hint ?? "Lowercase letters, digits, and single hyphens.")}
    >
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className="font-mono"
        disabled={!isNew}
        autoComplete="off"
        spellCheck={false}
        autoFocus={autoFocus}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${id}-error` : undefined}
      />
      <FieldError id={`${id}-error`}>{error}</FieldError>
    </FlowField>
  );
}

/* ── Choice cards ─────────────────────────────────────────────── */

export type ChoiceOption<T extends string> = {
  value: T;
  label: string;
  description?: React.ReactNode;
  icon?: React.ComponentType<{ className?: string }>;
  /** Tints the selected state; use for dangerous options. */
  tone?: "danger" | "warning";
};

/**
 * Radio group rendered as cards with a one-line description per option, for
 * enumerations whose meaning matters (permission mode, egress, tool access).
 * Arrow keys move the selection; only the selected card is in the tab order.
 */
export function ChoiceCards<T extends string>({
  value,
  onChange,
  options,
  columns = 3,
  "aria-label": ariaLabel,
  "aria-labelledby": ariaLabelledBy,
  disabled,
  compact,
}: {
  value: T;
  onChange: (value: T) => void;
  options: ChoiceOption<T>[];
  columns?: 2 | 3 | 4;
  "aria-label"?: string;
  "aria-labelledby"?: string;
  disabled?: boolean;
  compact?: boolean;
}) {
  const refs = React.useRef<Array<HTMLButtonElement | null>>([]);
  const selectedIndex = Math.max(0, options.findIndex((option) => option.value === value));

  function move(from: number, delta: number) {
    const next = (from + delta + options.length) % options.length;
    onChange(options[next].value);
    refs.current[next]?.focus();
  }

  const gridCols = { 2: "sm:grid-cols-2", 3: "sm:grid-cols-3", 4: "sm:grid-cols-2 lg:grid-cols-4" }[columns];
  return (
    <div role="radiogroup" aria-label={ariaLabel} aria-labelledby={ariaLabelledBy} className={cn("grid gap-2", gridCols)}>
      {options.map((option, index) => {
        const selected = option.value === value;
        const Icon = option.icon;
        return (
          <button
            key={option.value}
            ref={(element) => {
              refs.current[index] = element;
            }}
            type="button"
            role="radio"
            aria-checked={selected}
            tabIndex={selected || (selectedIndex === -1 && index === 0) ? 0 : -1}
            disabled={disabled}
            onClick={() => onChange(option.value)}
            onKeyDown={(event) => {
              if (event.key === "ArrowRight" || event.key === "ArrowDown") {
                event.preventDefault();
                move(index, 1);
              } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
                event.preventDefault();
                move(index, -1);
              }
            }}
            className={cn(
              "flex min-w-0 flex-col items-start gap-0.5 rounded-lg border px-3 text-left transition-colors",
              compact ? "py-1.5" : "py-2.5",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
              "disabled:cursor-not-allowed disabled:opacity-60",
              selected
                ? option.tone === "danger"
                  ? "border-[color-mix(in_oklch,var(--tone-danger)_45%,transparent)] bg-[color-mix(in_oklch,var(--tone-danger)_10%,transparent)]"
                  : option.tone === "warning"
                    ? "border-[color-mix(in_oklch,var(--tone-warning)_45%,transparent)] bg-[color-mix(in_oklch,var(--tone-warning)_10%,transparent)]"
                    : "border-primary/50 bg-primary/8"
                : "border-border/70 bg-transparent hover:border-ring/40 hover:bg-muted/40",
            )}
          >
            <span className="flex w-full min-w-0 items-center gap-1.5">
              {Icon && <Icon className={cn("size-3.5 shrink-0", selected ? "text-foreground" : "text-muted-foreground")} />}
              <span className={cn("truncate text-[12.5px] font-medium", selected ? "text-foreground" : "text-foreground/90")}>{option.label}</span>
              <span
                aria-hidden
                className={cn(
                  "ml-auto size-3.5 shrink-0 rounded-full border transition-colors",
                  selected
                    ? option.tone === "danger"
                      ? "border-[color:var(--tone-danger)] bg-[color:var(--tone-danger)] ring-2 ring-inset ring-background"
                      : option.tone === "warning"
                        ? "border-[color:var(--tone-warning)] bg-[color:var(--tone-warning)] ring-2 ring-inset ring-background"
                        : "border-primary bg-primary ring-2 ring-inset ring-background"
                    : "border-border",
                )}
              />
            </span>
            {option.description && !compact && (
              <span className="text-[11.5px] leading-snug text-muted-foreground">{option.description}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

/* ── Key / value rows ─────────────────────────────────────────── */

/**
 * Editable map: one key input and one value input per row. Keeps blank rows
 * while the user types and reports empty or duplicate keys inline, so the
 * parent can gate saving on `keyValueRowsError`.
 */
export function KeyValueRows({
  rows,
  onChange,
  keyLabel = "Key",
  valueLabel = "Value",
  keyPlaceholder = "KEY",
  valuePlaceholder = "value",
  addLabel = "Add variable",
  keySuggestions,
  mono = true,
  emptyHint,
}: {
  rows: KeyValueRow[];
  onChange: (rows: KeyValueRow[]) => void;
  keyLabel?: string;
  valueLabel?: string;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  addLabel?: string;
  /** Quick-add chips for well-known keys (cpu, memory). */
  keySuggestions?: string[];
  mono?: boolean;
  emptyHint?: React.ReactNode;
}) {
  const error = keyValueRowsError(rows, keyLabel);
  const usedKeys = new Set(rows.map((row) => row.key.trim()));
  const update = (index: number, patch: Partial<KeyValueRow>) =>
    onChange(rows.map((row, current) => (current === index ? { ...row, ...patch } : row)));
  return (
    <div className="space-y-2">
      {rows.length === 0 && emptyHint && <p className="text-[11.5px] text-muted-foreground">{emptyHint}</p>}
      {rows.map((row, index) => (
        <div key={index} className="flex items-center gap-2">
          <Input
            aria-label={`${keyLabel} ${index + 1}`}
            value={row.key}
            onChange={(event) => update(index, { key: event.target.value })}
            placeholder={keyPlaceholder}
            className={cn("w-[40%] min-w-0", mono && "font-mono")}
            autoComplete="off"
            spellCheck={false}
          />
          <Input
            aria-label={`${valueLabel} ${index + 1}`}
            value={row.value}
            onChange={(event) => update(index, { value: event.target.value })}
            placeholder={valuePlaceholder}
            className={cn("min-w-0 flex-1", mono && "font-mono")}
            autoComplete="off"
            spellCheck={false}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`Remove ${row.key.trim() || `${keyLabel.toLowerCase()} ${index + 1}`}`}
            className="shrink-0 text-muted-foreground hover:text-destructive"
            onClick={() => onChange(rows.filter((_, current) => current !== index))}
          >
            <X />
          </Button>
        </div>
      ))}
      <div className="flex flex-wrap items-center gap-1.5">
        <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rows, { key: "", value: "" }])}>
          <Plus data-icon="inline-start" />
          {addLabel}
        </Button>
        {keySuggestions
          ?.filter((key) => !usedKeys.has(key))
          .map((key) => (
            <Button
              key={key}
              type="button"
              variant="ghost"
              size="sm"
              className="font-mono text-[11px] text-muted-foreground"
              onClick={() => onChange([...rows, { key, value: "" }])}
            >
              + {key}
            </Button>
          ))}
      </div>
      <FieldError>{error}</FieldError>
    </div>
  );
}

/* ── Token input ──────────────────────────────────────────────── */

/**
 * Chips + text input for short string lists (tool names, env var names).
 * Enter, comma, or blur commits the pending text; Backspace on an empty
 * input removes the last chip.
 */
export function TokenInput({
  id,
  value,
  onChange,
  placeholder,
  mono = true,
  validate,
  "aria-label": ariaLabel,
}: {
  id?: string;
  value: string[];
  onChange: (value: string[]) => void;
  placeholder?: string;
  mono?: boolean;
  /** Returns an error for a token that must not be added. */
  validate?: (token: string) => string | null;
  "aria-label"?: string;
}) {
  const [pending, setPending] = React.useState("");
  const [error, setError] = React.useState<string | null>(null);

  function commit(raw: string) {
    const tokens = raw.split(/[,\s]+/).map((token) => token.trim()).filter(Boolean);
    if (tokens.length === 0) {
      setPending("");
      return;
    }
    for (const token of tokens) {
      const problem = validate?.(token);
      if (problem) {
        setError(problem);
        return;
      }
    }
    setError(null);
    onChange([...value, ...tokens.filter((token) => !value.includes(token))]);
    setPending("");
  }

  return (
    <div className="space-y-1.5">
      <div
        className={cn(
          "flex min-h-8 w-full flex-wrap items-center gap-1.5 rounded-lg border border-input bg-transparent px-2 py-1 transition-colors",
          "focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/50 dark:bg-input/30",
        )}
      >
        {value.map((token) => (
          <span
            key={token}
            className={cn(
              "inline-flex h-6 items-center gap-1 rounded-md bg-muted px-1.5 text-[11.5px] text-foreground ring-1 ring-inset ring-border/60",
              mono && "font-mono text-[11px]",
            )}
          >
            {token}
            <button
              type="button"
              aria-label={`Remove ${token}`}
              onClick={() => onChange(value.filter((current) => current !== token))}
              className="rounded-sm text-muted-foreground hover:text-destructive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <input
          id={id}
          aria-label={ariaLabel}
          value={pending}
          onChange={(event) => {
            setError(null);
            setPending(event.target.value);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" || event.key === ",") {
              event.preventDefault();
              commit(pending);
            } else if (event.key === "Backspace" && !pending && value.length) {
              onChange(value.slice(0, -1));
            }
          }}
          onBlur={() => pending.trim() && commit(pending)}
          placeholder={value.length ? "" : placeholder}
          className={cn(
            "h-6 min-w-[8ch] flex-1 bg-transparent text-[12.5px] outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed",
            mono && "font-mono text-[12px]",
          )}
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      <FieldError>{error}</FieldError>
    </div>
  );
}

/* ── Switch row inside a FormSection ──────────────────────────── */

export function SwitchRow({
  id,
  label,
  hint,
  checked,
  onCheckedChange,
  disabled,
}: {
  id: string;
  label: string;
  hint?: React.ReactNode;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <label htmlFor={id} className="text-[12.5px] font-medium leading-none">
          {label}
        </label>
        {hint && <p className="mt-1 max-w-[56ch] text-[11px] leading-relaxed text-muted-foreground">{hint}</p>}
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} className="mt-0.5 shrink-0" />
    </div>
  );
}
