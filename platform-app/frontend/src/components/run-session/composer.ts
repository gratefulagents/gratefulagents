/** One focus treatment for every control in the composer. */
export const composerFocusRing =
  "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring";

/** Stable id for the n-th option of a listbox, for `aria-activedescendant`. */
export function optionId(listboxId: string, index: number): string {
  return `${listboxId}-option-${index}`;
}
