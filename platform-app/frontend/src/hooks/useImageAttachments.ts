import { useCallback, useRef, useState } from "react";

// Composer attachments are held as data URLs until the message RPC is sent.
export interface ImageAttachment {
  id: string;
  name: string;
  dataUrl: string;
}

export interface VideoAttachment {
  id: string;
  name: string;
  dataUrl: string;
}

const MAX_IMAGES = 8;
const MAX_VIDEOS = 1;
const MAX_IMAGE_BYTES = 20 * 1024 * 1024; // 20 MiB
const MAX_VIDEO_BYTES = 20 * 1024 * 1024; // 20 MiB
// The dashboard RPC read limit is 32 MiB. Reserve 2 MiB for the prompt and
// protobuf framing instead of allowing attachment data URLs to consume it all.
const MAX_TOTAL_ATTACHMENT_DATA_URL_BYTES = 30 * 1024 * 1024; // 30 MiB
const VIDEO_TYPES = new Set(["video/mp4", "video/quicktime", "video/webm"]);

type AttachmentCandidate =
  | { kind: "image"; attachment: ImageAttachment }
  | { kind: "video"; attachment: VideoAttachment };

function readFileAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error ?? new Error("failed to read file"));
    reader.readAsDataURL(file);
  });
}

// useImageAttachments manages pending image and video attachments for a chat
// composer, with helpers to add from a file picker or a paste event.
export function useImageAttachments() {
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const imagesRef = useRef<ImageAttachment[]>([]);
  const [videos, setVideos] = useState<VideoAttachment[]>([]);
  const videosRef = useRef<VideoAttachment[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [processing, setProcessing] = useState(false);
  const processingCountRef = useRef(0);

  const append = useCallback((candidates: AttachmentCandidate[], initialError: string | null = null) => {
    const nextImages = [...imagesRef.current];
    const nextVideos = [...videosRef.current];
    let totalBytes = [...nextImages, ...nextVideos].reduce(
      (total, attachment) => total + attachment.dataUrl.length,
      0,
    );
    let nextError = initialError;

    for (const candidate of candidates) {
      if (candidate.kind === "image" && nextImages.length >= MAX_IMAGES) {
        nextError = `You can attach up to ${MAX_IMAGES} images.`;
        continue;
      }
      if (candidate.kind === "video" && nextVideos.length >= MAX_VIDEOS) {
        nextError = "You can attach up to 1 video.";
        continue;
      }
      // A video needs at least one of the eight model image slots after it is
      // converted into frames by the server.
      if (nextImages.length + nextVideos.length >= MAX_IMAGES) {
        nextError = `You can attach up to ${MAX_IMAGES} images or videos combined.`;
        continue;
      }
      if (totalBytes + candidate.attachment.dataUrl.length > MAX_TOTAL_ATTACHMENT_DATA_URL_BYTES) {
        nextError = "Attachments are too large together (max 30 MB).";
        continue;
      }
      if (candidate.kind === "image") {
        nextImages.push(candidate.attachment);
      } else {
        nextVideos.push(candidate.attachment);
      }
      totalBytes += candidate.attachment.dataUrl.length;
    }

    if (nextImages.length !== imagesRef.current.length) {
      imagesRef.current = nextImages;
      setImages(nextImages);
    }
    if (nextVideos.length !== videosRef.current.length) {
      videosRef.current = nextVideos;
      setVideos(nextVideos);
    }
    setError(nextError);
  }, []);

  const addFiles = useCallback(async (files: FileList | File[] | null | undefined) => {
    if (!files || files.length === 0) return;
    processingCountRef.current += 1;
    setProcessing(true);
    try {
      const added: AttachmentCandidate[] = [];
      let nextError: string | null = null;
      for (const file of Array.from(files)) {
        const kind = file.type.startsWith("image/")
          ? "image"
          : VIDEO_TYPES.has(file.type)
            ? "video"
            : null;
        if (!kind) {
          nextError = `"${file.name || "file"}" is not a supported image or video. Choose an image, MP4, MOV, or WebM video.`;
          continue;
        }
        const maxBytes = kind === "image" ? MAX_IMAGE_BYTES : MAX_VIDEO_BYTES;
        if (file.size > maxBytes) {
          nextError = `"${file.name || kind}" is too large (max 20 MB).`;
          continue;
        }
        try {
          const dataUrl = await readFileAsDataURL(file);
          const attachment = {
            id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
            name: file.name || `pasted-${kind}`,
            dataUrl,
          };
          added.push({ kind, attachment });
        } catch {
          nextError = `Failed to read the ${kind}.`;
        }
      }
      append(added, nextError);
    } finally {
      processingCountRef.current -= 1;
      if (processingCountRef.current === 0) setProcessing(false);
    }
  }, [append]);

  // Returns true when at least one supported clipboard file was handled so the
  // caller can suppress default text paste behavior.
  const onPaste = useCallback(
    (e: React.ClipboardEvent): boolean => {
      const files: File[] = [];
      for (const item of Array.from(e.clipboardData?.items ?? [])) {
        if (item.kind === "file" && (item.type.startsWith("image/") || VIDEO_TYPES.has(item.type))) {
          const file = item.getAsFile();
          if (file) files.push(file);
        }
      }
      if (files.length === 0) return false;
      void addFiles(files);
      return true;
    },
    [addFiles],
  );

  // Restore only images because videos are converted before message storage.
  const addDataUrls = useCallback((dataUrls: string[] | undefined) => {
    const restored = (dataUrls ?? [])
      .filter((url) => url.startsWith("data:image/"))
      .map((dataUrl, index) => ({
        id: `${Date.now()}-${Math.random().toString(36).slice(2)}`,
        name: `image-${index + 1}`,
        dataUrl,
      }));
    if (restored.length === 0) return;
    append(restored.map((attachment) => ({ kind: "image" as const, attachment })));
  }, [append]);

  const remove = useCallback((id: string) => {
    const nextImages = imagesRef.current.filter((image) => image.id !== id);
    const nextVideos = videosRef.current.filter((video) => video.id !== id);
    imagesRef.current = nextImages;
    videosRef.current = nextVideos;
    setImages(nextImages);
    setVideos(nextVideos);
    setError(null);
  }, []);

  const clear = useCallback(() => {
    imagesRef.current = [];
    videosRef.current = [];
    setImages([]);
    setVideos([]);
    setError(null);
  }, []);

  const dataUrls = useCallback(() => images.map((image) => image.dataUrl), [images]);
  const videoDataUrls = useCallback(() => videos.map((video) => video.dataUrl), [videos]);

  return {
    images,
    videos,
    error,
    processing,
    addFiles,
    addDataUrls,
    onPaste,
    remove,
    clear,
    dataUrls,
    videoDataUrls,
  };
}
