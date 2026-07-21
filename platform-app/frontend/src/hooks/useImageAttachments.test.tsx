import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useImageAttachments } from "./useImageAttachments";

function imageFile(name: string): File {
  return new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
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

  it("rejects additions that would exceed the aggregate encoded-size limit", () => {
    const { result } = renderHook(() => useImageAttachments());
    const sixteenMiBDataUrl = `data:image/png;base64,${"A".repeat(16 * 1024 * 1024)}`;

    act(() => {
      result.current.addDataUrls([sixteenMiBDataUrl, sixteenMiBDataUrl]);
    });

    expect(result.current.images).toHaveLength(1);
    expect(result.current.error).toBe("Images are too large together (max 30 MB).");
  });
});
