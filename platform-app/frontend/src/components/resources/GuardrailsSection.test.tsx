import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { GuardrailsSection } from "@/components/resources/GuardrailsSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listGuardrailPolicies: vi.fn(),
    createGuardrailPolicy: vi.fn().mockResolvedValue({}),
    updateGuardrailPolicy: vi.fn().mockResolvedValue({}),
    deleteGuardrailPolicy: vi.fn().mockResolvedValue({}),
  },
}));

const listGuardrailPolicies = vi.mocked(client.listGuardrailPolicies);

const policy = {
  name: "no-secrets",
  namespace: "team-a",
  rules: [
    { name: "aws-keys", type: "tool-output", toolPattern: "*", regex: "AKIA[0-9A-Z]{16}", action: "block", message: "AWS keys must not be printed." },
    { name: "rm-rf", type: "tool-input", toolPattern: "bash*", regex: "rm\\s+-rf", action: "warn", message: "" },
  ],
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("GuardrailsSection", () => {
  it("lists policies with rule counts and actions", async () => {
    listGuardrailPolicies.mockResolvedValue({ policies: [policy] } as never);
    render(<GuardrailsSection />);

    expect(await screen.findByText("no-secrets")).toBeTruthy();
    expect(screen.getByText("2 rules")).toBeTruthy();
    expect(screen.getByText("aws-keys, rm-rf")).toBeTruthy();
    expect(screen.getByText("1 block")).toBeTruthy();
    expect(screen.getByText("1 warn")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit no-secrets" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete no-secrets" })).toBeTruthy();
  });

  it("creates a policy and rejects an invalid pattern until it is fixed", async () => {
    listGuardrailPolicies.mockResolvedValue({ policies: [] } as never);
    render(<GuardrailsSection />);

    fireEvent.click((await screen.findAllByRole("button", { name: "New policy" }))[0]);
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "block-rm" } });
    fireEvent.change(screen.getByLabelText(/^Rule name/), { target: { value: "no-rm" } });
    fireEvent.change(screen.getByLabelText(/^Tool pattern/), { target: { value: "bash*" } });
    fireEvent.change(screen.getByLabelText(/^Pattern/), { target: { value: "rm\\s+-rf(" } });

    const create = screen.getByRole("button", { name: "Create policy" });
    expect(screen.getByRole("alert").textContent).toMatch(/Unterminated group|\(/);
    expect((create as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(screen.getByLabelText(/^Pattern/), { target: { value: "rm\\s+-rf" } });
    fireEvent.click(screen.getByRole("radio", { name: "Warn" }));
    fireEvent.change(screen.getByLabelText(/^Message/), { target: { value: "Careful." } });
    expect((create as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(create);

    await waitFor(() => {
      expect(client.createGuardrailPolicy).toHaveBeenCalledWith({
        policy: expect.objectContaining({
          name: "block-rm",
          rules: [
            expect.objectContaining({ name: "no-rm", type: "tool-input", toolPattern: "bash*", regex: "rm\\s+-rf", action: "warn", message: "Careful." }),
          ],
        }),
      });
    });
    expect(listGuardrailPolicies).toHaveBeenCalledTimes(2);
  });

  it("edits an existing policy and only enables saving once something changed", async () => {
    listGuardrailPolicies.mockResolvedValue({ policies: [policy] } as never);
    render(<GuardrailsSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Edit no-secrets" }));
    const save = screen.getByRole("button", { name: "Save changes" });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByLabelText(/^Name/) as HTMLInputElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Remove rule 2" }));
    expect((save as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(save);

    await waitFor(() => {
      expect(client.updateGuardrailPolicy).toHaveBeenCalledWith({
        policy: expect.objectContaining({ name: "no-secrets", rules: [expect.objectContaining({ name: "aws-keys" })] }),
      });
    });
  });

  it("asks before discarding unsaved edits", async () => {
    listGuardrailPolicies.mockResolvedValue({ policies: [policy] } as never);
    render(<GuardrailsSection />);

    fireEvent.click(await screen.findByRole("button", { name: "Edit no-secrets" }));
    fireEvent.change(screen.getAllByLabelText(/^Message/)[0], { target: { value: "changed" } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(await screen.findByText("Discard changes?")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(screen.queryByText("Discard changes?")).toBeNull());
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });
});
