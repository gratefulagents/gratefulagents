import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { SecurityProgramDialog } from "@/components/SecurityProgramDialog";
import { SecurityProgramResourceSchema } from "@/rpc/platform/service_pb";

const { createSecurityProgram, updateSecurityProgram } = vi.hoisted(() => ({
  createSecurityProgram: vi.fn(),
  updateSecurityProgram: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { createSecurityProgram, updateSecurityProgram },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SecurityProgramDialog", () => {
  it("creates an explicit verified scope snapshot", async () => {
    createSecurityProgram.mockResolvedValue({});
    render(
      <SecurityProgramDialog
        trigger={<button>New program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));

    expect(screen.getByText(/URL is provenance only and does not authorize network testing/i)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "acme-bounty" } });
    fireEvent.change(screen.getByLabelText(/^Provider/), { target: { value: "HackerOne" } });
    fireEvent.change(screen.getByLabelText(/^Display name/), {
      target: { value: "Acme public bug bounty" },
    });
    fireEvent.change(screen.getByLabelText(/^Program URL/), {
      target: { value: "https://hackerone.com/acme" },
    });
    fireEvent.change(screen.getByLabelText(/^Scope policy snapshot/), {
      target: { value: "In scope:\n- api.example.com\n\nOut of scope:\n- denial-of-service" },
    });
    fireEvent.change(screen.getByLabelText(/^Verified at/), {
      target: { value: "2026-03-01T12:00" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create security program" }));

    await waitFor(() => expect(createSecurityProgram).toHaveBeenCalledTimes(1));
    const program = createSecurityProgram.mock.calls[0][0].program;
    expect(program).toMatchObject({
      name: "acme-bounty",
      provider: "HackerOne",
      displayName: "Acme public bug bounty",
      programUrl: "https://hackerone.com/acme",
      scopePolicy: "In scope:\n- api.example.com\n\nOut of scope:\n- denial-of-service",
    });
    expect(program.verifiedAt).toBeTruthy();
  });

  it("rejects a non-HTTPS provenance URL", () => {
    render(
      <SecurityProgramDialog
        trigger={<button>New program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));
    fireEvent.change(screen.getByLabelText(/^Program URL/), {
      target: { value: "http://hackerone.com/acme" },
    });

    expect(screen.getByText(/absolute HTTPS URL/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("round-trips every editable field and verified timestamp", async () => {
    const verifiedAt = timestampFromDate(new Date("2026-03-01T12:00:37Z"));
    const source = create(SecurityProgramResourceSchema, {
      namespace: "user-alice",
      name: "acme-bounty",
      provider: "HackerOne",
      displayName: "Acme public bug bounty",
      programUrl: "https://hackerone.com/acme",
      scopePolicy: "In scope: api.example.com",
      verifiedAt,
    });
    updateSecurityProgram.mockResolvedValue({});
    const view = render(
      <SecurityProgramDialog
        source={source}
        trigger={<button>Edit program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit program" }));

    expect((screen.getByLabelText(/^Name/) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText(/^Provider/) as HTMLInputElement).value).toBe("HackerOne");
    expect((screen.getByLabelText(/^Scope policy snapshot/) as HTMLTextAreaElement).value).toBe(
      "In scope: api.example.com",
    );
    fireEvent.click(screen.getByRole("button", { name: "Save security program" }));

    await waitFor(() => expect(updateSecurityProgram).toHaveBeenCalledTimes(1));
    const program = updateSecurityProgram.mock.calls[0][0].program;
    expect(program.namespace).toBe("user-alice");
    expect(program.name).toBe("acme-bounty");
    expect(timestampDate(program.verifiedAt).toISOString()).toBe("2026-03-01T12:00:37.000Z");

    const refreshed = create(SecurityProgramResourceSchema, {
      ...source,
      displayName: "Acme updated bounty",
      scopePolicy: "Updated in-scope assets",
      generation: 2n,
    });
    view.rerender(
      <SecurityProgramDialog
        source={refreshed}
        trigger={<button>Edit program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit program" }));
    expect((screen.getByLabelText(/^Display name/) as HTMLInputElement).value).toBe("Acme updated bounty");
    expect((screen.getByLabelText(/^Scope policy snapshot/) as HTMLTextAreaElement).value).toBe("Updated in-scope assets");
  });
});
