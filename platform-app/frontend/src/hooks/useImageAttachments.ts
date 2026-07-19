import { useCallback, useState } from "react";

// ImageAttachment is a user-attached image held in the composer prior to
// sending. dataUrl is a full "data:<mime>;base64,..." string ready to pass to
// the sendAgentRunMessage RPC.
export interface ImageAttachment {
  id: string;
  name: string;
  dataUrl: string;
}

// Cap individual attachments so we never try to ship a huge blob through the
// RPC layer. Mirrors the server-side limit.
const MAX_IMAGE_BYTES = 20 * 1024 * 1024; // 20 MiB

function readFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error ?? new Error("failed to read file"));
    reader.readAsDataURL(file);
  });
}

// useImageAttachments manages a small list of pending image attachments for a
// chat composer, with helpers to add from a file picker or a paste event.
export function useImageAttachments() {
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const [error, setError] = useState<string | null>(null);

  const addFiles = useCallback(async (files: FileList | File[] | null | undefined) => {
    if (!files) return;
    const list = Array.from(files).filter((f) => f.type.startsWith("image/"));
    if (list.length === 0) return;
    const added: ImageAttachment[] = [];
    for (const file of list) {
      if (file.size > MAX_IMAGE_BYTES) {
        setError(`"${file.name || "image"}" is too large (max 20 MB).`);
        continue;
      }
      try {
        const dataUrl = await readFileAsDataURL(file);
        added.push({
          id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
          name: file.name || "pasted-image",
          dataUrl,
        });
      } catch {
        setError("Failed to read an image.");
      }
    }
    if (added.length > 0) {
      setError(null);
      setImages((prev) => [...prev, ...added]);
    }
  }, []);

  // onPaste extracts images from the clipboard. Returns true when at least one
  // image was handled so the caller can suppress default text paste behavior.
  const onPaste = useCallback(
    (e: React.ClipboardEvent): boolean => {
      const files: File[] = [];
      for (const item of Array.from(e.clipboardData?.items ?? [])) {
        if (item.kind === "file" && item.type.startsWith("image/")) {
          const f = item.getAsFile();
          if (f) files.push(f);
        }
      }
      if (files.length === 0) return false;
      void addFiles(files);
      return true;
    },
    [addFiles],
  );

  // addDataUrls restores attachments from already-encoded data URLs (e.g. when
  // pulling a cancelled pending message back into the composer for editing).
  const addDataUrls = useCallback((dataUrls: string[] | undefined) => {
    const restored = (dataUrls ?? [])
      .filter((url) => url.startsWith("data:"))
      .map((dataUrl, index) => ({
        id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
        name: `image-${index + 1}`,
        dataUrl,
      }));
    if (restored.length === 0) return;
    setError(null);
    setImages((prev) => [...prev, ...restored]);
  }, []);

  const remove = useCallback((id: string) => {
    setImages((prev) => prev.filter((img) => img.id !== id));
  }, []);

  const clear = useCallback(() => {
    setImages([]);
    setError(null);
  }, []);

  const dataUrls = useCallback(() => images.map((img) => img.dataUrl), [images]);

  return { images, error, addFiles, addDataUrls, onPaste, remove, clear, dataUrls };
}
