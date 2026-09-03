import type { CSSProperties } from "react";

/**
 * Deterministic monogram styling for projects, workspaces and users.
 *
 * A name always maps to the same hue so a project tile is recognisable at a
 * glance (and across sessions) without storing any colour preference. Hues
 * are expressed in oklch to stay perceptually even in both themes.
 */

/** Stable 32-bit FNV-1a hash of a string. */
function fnv1a(input: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < input.length; i++) {
    hash ^= input.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

/** Hue in degrees [0, 360) derived from the name. */
export function monogramHue(name: string): number {
  return fnv1a(name.toLowerCase()) % 360;
}

/**
 * One or two uppercase characters for a tile. Multi-word names use the first
 * letter of the first two words ("Grateful Agents" → "GA"); single words use
 * the first two letters ("gateway" → "GA"); punctuation is skipped.
 */
export function monogramInitials(name: string): string {
  const words = name
    .split(/[\s\-_./]+/)
    .map((w) => w.replace(/[^\p{L}\p{N}]/gu, ""))
    .filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}

/**
 * CSS custom properties for a tile. Consumers use `--mono` for the accent
 * (text/ring) and `--mono-bg` for the tinted fill.
 */
export function monogramStyle(name: string): CSSProperties {
  const hue = monogramHue(name);
  return {
    "--mono": `oklch(var(--mono-l, 0.74) var(--mono-c, 0.12) ${hue})`,
    "--mono-bg": `oklch(var(--mono-l, 0.74) var(--mono-c, 0.12) ${hue} / var(--mono-bg-alpha, 0.14))`,
  } as CSSProperties;
}
