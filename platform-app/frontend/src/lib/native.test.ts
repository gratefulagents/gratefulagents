import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { invokeMock, openUrlMock } = vi.hoisted(() => ({
  invokeMock: vi.fn(),
  openUrlMock: vi.fn(),
}));

vi.mock("./platform", () => ({
  isTauri: true,
  platform: vi.fn().mockResolvedValue("macos"),
}));

vi.mock("@tauri-apps/plugin-opener", () => ({
  openUrl: openUrlMock,
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: invokeMock,
}));

import {
  DIAGNOSTICS_EMAIL_URL,
  DIAGNOSTICS_ISSUE_URL,
  installExternalLinkInterceptor,
  openDiagnosticLogs,
} from "./native";

describe("installExternalLinkInterceptor", () => {
  beforeEach(() => {
    invokeMock.mockResolvedValue(undefined);
    openUrlMock.mockResolvedValue(undefined);
  });

  afterEach(() => {
    invokeMock.mockReset();
    openUrlMock.mockReset();
    document.body.replaceChildren();
  });

  it("opens a pull request once when the interceptor is installed repeatedly", async () => {
    const cleanup = installExternalLinkInterceptor();
    const repeatedCleanup = installExternalLinkInterceptor();

    try {
      expect(repeatedCleanup).toBe(cleanup);

      const link = document.createElement("a");
      link.href = "https://github.com/gratefulagents/gratefulagents/pull/123";
      link.target = "_blank";
      link.textContent = "View pull request";
      document.body.append(link);

      const shouldNavigate = link.dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }),
      );

      expect(shouldNavigate).toBe(false);
      await vi.waitFor(() => expect(openUrlMock).toHaveBeenCalledTimes(1));
      expect(openUrlMock).toHaveBeenCalledWith(link.href);
    } finally {
      cleanup();
    }
  });

  it("opens the native app log directory on desktop", async () => {
    await openDiagnosticLogs();

    expect(invokeMock).toHaveBeenCalledWith("open_log_directory");
    expect(openUrlMock).not.toHaveBeenCalled();
  });

  it("uses the gratefulagents issue tracker and private diagnostics mailbox", () => {
    expect(DIAGNOSTICS_ISSUE_URL).toBe(
      "https://github.com/gratefulagents/gratefulagents/issues/new",
    );
    expect(DIAGNOSTICS_EMAIL_URL).toContain("captaintrips@gratefulagents.dev");
  });
});
