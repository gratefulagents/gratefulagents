// Client-side file download helper.
//
// Creates a temporary object URL for the given bytes and clicks a hidden
// anchor to trigger the browser's download flow. The Tauri external-link
// interceptor in native.ts deliberately ignores anchors carrying a
// `download` attribute, so the same path is used inside the desktop webview
// on platforms whose webview supports downloads.

/** Trigger a client-side download of `data` under `filename`. */
export function downloadBlob(
  filename: string,
  data: Uint8Array | Blob,
  mimeType = "application/octet-stream",
): void {
  const blob =
    data instanceof Blob ? data : new Blob([data as BlobPart], { type: mimeType });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  // Revoke after the download has had a chance to start; revoking too early
  // aborts the save in some browsers.
  window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
