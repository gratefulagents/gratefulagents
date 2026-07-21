import { useCallback, useRef, useState } from "react";

// ImageAttachment is a user-attached image held in the composer prior to
// sending. dataUrl is a full "data:<mime>;base64,..." string ready to pass to
// the sendAgentRunMessage RPC.
export interface ImageAttachment {
  id: string;
  name: string;
  dataUrl: string;
}

// Keep the pending payload within the backend parser and transport limits.
const MAX_IMAGES = 8;
const MAX_IMAGE_BYTES = 20 * 1024 * 1024; // 20 MiB
// The dashboard RPC read limit is 32 MiB. Reserve 2 MiB for the prompt and
// protobuf framing instead of allowing image data URLs to consume it all.
const MAX_TOTAL_IMAGE_DATA_URL_BYTES = 30 * 1024 * 1024; // 30 MiB

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
  const imagesRef = useRef<ImageAttachment[]>([]);
  const [error, setError] = useState<string | null>(null);

  const append = useCallback((candidates: ImageAttachment[], initialError: string | null = null) => {
    const next = [...imagesRef.current];
    let totalBytes = next.reduce((total, image) => total + image.dataUrl.length, 0);
    let nextError = initialError;

    for (const candidate of candidates) {
      if (next.length >= MAX_IMAGES) {
        nextError = `You can attach up to ${MAX_IMAGES} images.`;
        break;
      }
      if (totalBytes + candidate.dataUrl.length > MAX_TOTAL_IMAGE_DATA_URL_BYTES) {
        nextError = "Images are too large together (max 30 MB).";
        continue;
      }
      next.push(candidate);
      totalBytes += candidate.dataUrl.length;
    }

    if (next.length !== imagesRef.current.length) {
      imagesRef.current = next;
      setImages(next);
    }
    setError(nextError);
  }, []);

  const addFiles = useCallback(async (files: FileList | File[] | null | undefined) => {
    if (!files) return;
    const list = Array.from(files).filter((f) => f.type.startsWith("image/"));
    if (list.length === 0) return;
    const added: ImageAttachment[] = [];
    let nextError: string | null = null;
    for (const file of list) {
      if (file.size > MAX_IMAGE_BYTES) {
        nextError = `"${file.name || "image"}" is too large (max 20 MB).`;
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
        nextError = "Failed to read an image.";
      }
    }
    append(added, nextError);
  }, [append]);

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
    append(restored);
  }, [append]);

  const remove = useCallback((id: string) => {
    const next = imagesRef.current.filter((img) => img.id !== id);
    imagesRef.current = next;
    setImages(next);
    setError(null);
  }, []);

  const clear = useCallback(() => {
    imagesRef.current = [];
    setImages([]);
    setError(null);
  }, []);

  const dataUrls = useCallback(() => images.map((img) => img.dataUrl), [images]);

  return { images, error, addFiles, addDataUrls, onPaste, remove, clear, dataUrls };
}
