import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";

import { ChoiceCards, KeyValueRows, TokenInput } from "@/components/resources/form-kit";
import type { KeyValueRow } from "@/components/resources/resource-helpers";

afterEach(cleanup);

describe("ChoiceCards", () => {
  it("renders a radio group and moves the selection with arrow keys", () => {
    const onChange = vi.fn();
    render(
      <ChoiceCards
        aria-label="Egress"
        value="restricted"
        onChange={onChange}
        options={[
          { value: "restricted", label: "Restricted" },
          { value: "unrestricted", label: "Unrestricted" },
          { value: "disabled", label: "Disabled" },
        ]}
      />,
    );
    const group = screen.getByRole("radiogroup", { name: "Egress" });
    expect(group).toBeTruthy();
    const selected = screen.getByRole("radio", { name: "Restricted" });
    expect(selected.getAttribute("aria-checked")).toBe("true");
    expect(selected.getAttribute("tabindex")).toBe("0");
    expect(screen.getByRole("radio", { name: "Disabled" }).getAttribute("tabindex")).toBe("-1");

    fireEvent.keyDown(selected, { key: "ArrowRight" });
    expect(onChange).toHaveBeenCalledWith("unrestricted");
    fireEvent.keyDown(selected, { key: "ArrowLeft" });
    expect(onChange).toHaveBeenCalledWith("disabled");
    fireEvent.click(screen.getByRole("radio", { name: "Disabled" }));
    expect(onChange).toHaveBeenLastCalledWith("disabled");
  });
});

function KeyValueHarness({ initial }: { initial: KeyValueRow[] }) {
  const [rows, setRows] = useState(initial);
  return <KeyValueRows rows={rows} onChange={setRows} keyLabel="Variable" keySuggestions={["cpu", "memory"]} />;
}

describe("KeyValueRows", () => {
  it("adds rows from suggestions, flags duplicate keys, and removes rows", () => {
    render(<KeyValueHarness initial={[{ key: "cpu", value: "500m" }]} />);
    expect(screen.queryByRole("button", { name: "+ cpu" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "+ memory" }));
    expect((screen.getByLabelText("Variable 2") as HTMLInputElement).value).toBe("memory");

    fireEvent.change(screen.getByLabelText("Variable 2"), { target: { value: "cpu" } });
    expect(screen.getByRole("alert").textContent).toContain("Duplicate variable: cpu");

    fireEvent.click(screen.getAllByRole("button", { name: "Remove cpu" })[1]);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getAllByLabelText(/^Variable \d/).length).toBe(1);
  });

  it("ignores fully blank rows when validating", () => {
    render(<KeyValueHarness initial={[]} />);
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.change(screen.getByLabelText("Value 1"), { target: { value: "x" } });
    expect(screen.getByRole("alert").textContent).toContain("Variable names cannot be empty");
  });
});

function TokenHarness({ validate }: { validate?: (token: string) => string | null }) {
  const [value, setValue] = useState<string[]>(["ALPHA"]);
  return <TokenInput aria-label="Tokens" value={value} onChange={setValue} validate={validate} />;
}

describe("TokenInput", () => {
  it("commits on Enter and comma, dedupes, and removes with Backspace", () => {
    render(<TokenHarness />);
    const input = screen.getByLabelText("Tokens");
    fireEvent.change(input, { target: { value: "BETA" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByText("BETA")).toBeTruthy();

    fireEvent.change(input, { target: { value: "ALPHA, GAMMA" } });
    fireEvent.keyDown(input, { key: "," });
    expect(screen.getAllByText("ALPHA").length).toBe(1);
    expect(screen.getByText("GAMMA")).toBeTruthy();

    fireEvent.keyDown(input, { key: "Backspace" });
    expect(screen.queryByText("GAMMA")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Remove BETA" }));
    expect(screen.queryByText("BETA")).toBeNull();
  });

  it("refuses tokens the validator rejects", () => {
    render(<TokenHarness validate={(token) => (token.includes("/") ? "No slashes." : null)} />);
    const input = screen.getByLabelText("Tokens");
    fireEvent.change(input, { target: { value: "a/b" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("alert").textContent).toBe("No slashes.");
    expect(screen.queryByText("a/b")).toBeNull();
  });
});
