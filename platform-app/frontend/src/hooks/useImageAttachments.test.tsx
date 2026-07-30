import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useImageAttachments } from "./useImageAttachments";

function imageFile(name: string): File {
  return new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
}

function videoFile(name: string, bytes = new Uint8Array([1, 2, 3])): File {
  return new File([bytes], name, { type: "video/mp4" });
}

describe("useImageAttachments", () => {
  it("caps images at eight across multiple additions", async () => {
    const { result } = renderHook(() => useImageAttachments());

    await act(async () => {
      await result.current.addFiles(
        Array.from({ length: 7 }, (_, index) => imageFile(`image-${index + 1}.png`)),
      );
    });
    await act(async () => {
      await result.current.addFiles([imageFile("image-8.png"), imageFile("image-9.png")]);
    });

    expect(result.current.images).toHaveLength(8);
    expect(result.current.error).toBe("You can attach up to 8 images.");
  });

  it("accepts an allowed video", async () => {
    const { result } = renderHook(() => useImageAttachments());

    await act(async () => {
      await result.current.addFiles([videoFile("clip.mp4")]);
    });

    expect(result.current.images).toHaveLength(0);
    expect(result.current.videos).toHaveLength(1);
    expect(result.current.videoDataUrls()).toEqual([result.current.videos[0].dataUrl]);
    expect(result.current.processing).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it("rejects a second video", async () => {
    const { result } = renderHook(() => useImageAttachments());

    await act(async () => {
      await result.current.addFiles([videoFile("first.mp4")]);
      await result.current.addFiles([videoFile("second.mp4")]);
    });

    expect(result.current.videos).toHaveLength(1);
    expect(result.current.error).toBe("You can attach up to 1 video.");
  });

  it("rejects oversized and unsupported files", async () => {
    const { result } = renderHook(() => useImageAttachments());
    const oversized = videoFile("large.mp4", new Uint8Array(20 * 1024 * 1024 + 1));
    const unsupported = new File(["text"], "notes.txt", { type: "text/plain" });

    await act(async () => {
      await result.current.addFiles([oversized]);
    });
    expect(result.current.videos).toHaveLength(0);
    expect(result.current.error).toBe('"large.mp4" is too large (max 20 MB).');

    await act(async () => {
      await result.current.addFiles([unsupported]);
    });
    expect(result.current.error).toBe(
      '"notes.txt" is not a supported image or video. Choose an image, MP4, MOV, or WebM video.',
    );
  });

  it("reserves a visual slot for a video", async () => {
    const { result } = renderHook(() => useImageAttachments());

    await act(async () => {
      await result.current.addFiles([
        ...Array.from({ length: 7 }, (_, index) => imageFile(`image-${index + 1}.png`)),
        videoFile("clip.mp4"),
      ]);
      await result.current.addFiles([imageFile("image-8.png")]);
    });

    expect(result.current.images).toHaveLength(7);
    expect(result.current.videos).toHaveLength(1);
    expect(result.current.error).toBe("You can attach up to 8 images or videos combined.");
  });

  it("rejects additions that exceed the aggregate encoded-size limit", async () => {
    const { result } = renderHook(() => useImageAttachments());
    const sixteenMiBDataUrl = `data:image/png;base64,${"A".repeat(16 * 1024 * 1024)}`;

    act(() => {
      result.current.addDataUrls([sixteenMiBDataUrl]);
    });
    await act(async () => {
      await result.current.addFiles([videoFile("clip.mp4", new Uint8Array(11 * 1024 * 1024))]);
    });

    expect(result.current.images).toHaveLength(1);
    expect(result.current.videos).toHaveLength(0);
    expect(result.current.error).toBe("Attachments are too large together (max 30 MB).");
  });
});
