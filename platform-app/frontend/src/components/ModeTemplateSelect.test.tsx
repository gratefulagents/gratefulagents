import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ModeTemplateSelect } from "@/components/ModeTemplateSelect";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listModeTemplates: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ModeTemplateSelect", () => {
  it("loads the catalog and reports the selected project default", async () => {
    vi.mocked(client.listModeTemplates).mockResolvedValue({
      templates: [
        { name: "interactive", displayName: "Interactive", description: "Ask before important choices." },
        { name: "review", displayName: "Review", description: "Inspect changes without writing." },
        { name: "autopilot", displayName: "Autopilot", description: "Complete work autonomously." },
      ],
    } as Awaited<ReturnType<typeof client.listModeTemplates>>);
    const onChange = vi.fn();

    render(<ModeTemplateSelect id="mode" value="autopilot" onChange={onChange} />);

    expect(await screen.findByText("Complete work autonomously.")).toBeTruthy();
    const select = screen.getByRole("combobox") as HTMLSelectElement;
    expect(Array.from(select.options).map((option) => option.text)).toEqual([
      "Interactive (platform default)",
      "Autopilot (autopilot)",
      "Review (review)",
    ]);

    fireEvent.change(select, { target: { value: "review" } });
    expect(onChange).toHaveBeenCalledWith("review");
  });

  it("preserves a configured mode when the catalog cannot be loaded", async () => {
    vi.mocked(client.listModeTemplates).mockRejectedValue(new Error("offline"));

    render(<ModeTemplateSelect id="mode" value="team-custom" onChange={() => undefined} />);

    await waitFor(() => {
      expect(screen.getByText(/current selection is preserved/i)).toBeTruthy();
    });
    expect((screen.getByRole("combobox") as HTMLSelectElement).value).toBe("team-custom");
    expect(screen.getByRole("option", { name: "team-custom" })).toBeTruthy();
  });
});
