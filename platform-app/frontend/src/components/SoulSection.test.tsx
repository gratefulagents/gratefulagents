import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { SoulSection } from "@/components/SoulSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    getMySoul: vi.fn(),
    updateMySoul: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const getMySoul = vi.mocked(client.getMySoul);
const updateMySoul = vi.mocked(client.updateMySoul);

describe("SoulSection", () => {
  it("loads and displays the saved SOUL", async () => {
    getMySoul.mockResolvedValue({ content: "# My persona" } as never);

    render(<SoulSection />);

    const textarea = (await screen.findByLabelText("SOUL content")) as HTMLTextAreaElement;
    await waitFor(() => expect(textarea.value).toBe("# My persona"));
    expect(getMySoul).toHaveBeenCalledTimes(1);
  });

  it("shows a not-saved hint when no SOUL exists", async () => {
    getMySoul.mockResolvedValue({ content: "" } as never);

    render(<SoulSection />);

    expect(await screen.findByText(/Not saved yet/i)).toBeTruthy();
  });

  it("saves edited content (trimmed)", async () => {
    getMySoul.mockResolvedValue({ content: "" } as never);
    updateMySoul.mockResolvedValue({ content: "# Edited" } as never);

    render(<SoulSection />);

    const textarea = await screen.findByLabelText("SOUL content");
    fireEvent.change(textarea, { target: { value: "  # Edited  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save SOUL" }));

    await waitFor(() => {
      expect(updateMySoul).toHaveBeenCalledWith({ content: "# Edited" });
    });
    expect(await screen.findByText("SOUL saved")).toBeTruthy();
  });

  it("surfaces a save error", async () => {
    getMySoul.mockResolvedValue({ content: "" } as never);
    updateMySoul.mockRejectedValue(new Error("nope"));

    render(<SoulSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Save SOUL" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("nope");
  });
});
