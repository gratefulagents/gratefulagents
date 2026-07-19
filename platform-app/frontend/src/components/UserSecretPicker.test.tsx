import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { UserSecretKeyPicker, UserSecretPicker } from "@/components/UserSecretPicker";

describe("UserSecretPicker", () => {
  it("offers only the caller inventory and refreshes when opened", () => {
    const onChange = vi.fn();
    const onOpen = vi.fn();
    render(
      <UserSecretPicker
        ariaLabel="Secret"
        value=""
        secrets={[
          { name: "usercred-github", keys: ["token"] },
          { name: "webhook", keys: ["secret"] },
        ]}
        onChange={onChange}
        onOpen={onOpen}
      />,
    );

    const picker = screen.getByRole("combobox", { name: "Secret" });
    expect(screen.getByRole("option", { name: "webhook" })).toBeTruthy();
    fireEvent.pointerDown(picker);
    expect(onOpen).toHaveBeenCalledTimes(1);
    fireEvent.change(picker, { target: { value: "webhook" } });
    expect(onChange).toHaveBeenCalledWith("webhook");
  });

  it("derives key choices from the selected secret and preserves missing legacy refs", () => {
    render(
      <UserSecretKeyPicker
        ariaLabel="Secret key"
        value="legacy-key"
        secretName="integration"
        secrets={[{ name: "integration", keys: ["token", "url"] }]}
        onChange={() => undefined}
      />,
    );

    expect(screen.getByRole("option", { name: "legacy-key (not found)" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "token" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "url" })).toBeTruthy();
  });
});
