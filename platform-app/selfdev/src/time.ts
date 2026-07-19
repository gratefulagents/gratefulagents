// Deterministic clock for scenario fixtures and screenshots.
//
// Every fixture timestamp is derived from SCENARIO_NOW, and the screenshot
// harness installs the same instant as the page clock (page.clock.install),
// so relative times ("3m ago") render identically on every run.

/** The frozen "now" that all fixtures and page clocks share. */
export const SCENARIO_NOW = new Date("2026-02-11T17:30:00Z");

export function minutesAgo(minutes: number, from: Date = SCENARIO_NOW): Date {
  return new Date(from.getTime() - minutes * 60_000);
}

export function hoursAgo(hours: number, from: Date = SCENARIO_NOW): Date {
  return minutesAgo(hours * 60, from);
}

export function daysAgo(days: number, from: Date = SCENARIO_NOW): Date {
  return hoursAgo(days * 24, from);
}

/** Unix seconds as bigint (proto int64 fields). */
export function unix(date: Date): bigint {
  return BigInt(Math.floor(date.getTime() / 1000));
}

/** Unix microseconds as bigint (trace spans). */
export function unixMicros(date: Date): bigint {
  return BigInt(date.getTime()) * 1000n;
}
