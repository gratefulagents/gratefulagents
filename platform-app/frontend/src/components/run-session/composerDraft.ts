/** A draft that has been submitted and whose send has not settled yet. */
export type SubmittedDraft = { key: string; value: string };

export function readDraft(key: string): string {
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

/**
 * Persists the composer draft. While a send is in flight the composer still
 * holds the submitted text; flushing it (debounce, run switch, unmount) would
 * resurrect an already-sent message on the next mount, so it is skipped.
 */
export function persistDraft(key: string, value: string, submitted?: SubmittedDraft | null) {
  if (submitted && submitted.key === key && submitted.value === value) {
    return;
  }
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    // Ignore quota / storage failures.
  }
}

/** Clears the stored draft as a send starts; see `restoreDraftAfterFailedSend`. */
export function beginDraftSend(key: string, value: string): SubmittedDraft {
  persistDraft(key, "");
  return { key, value };
}

/** Puts a failed send's text back into storage unless something newer was saved meanwhile. */
export function restoreDraftAfterFailedSend(submitted: SubmittedDraft) {
  if (readDraft(submitted.key) === "") {
    persistDraft(submitted.key, submitted.value);
  }
}
