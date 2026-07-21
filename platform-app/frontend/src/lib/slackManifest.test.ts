import { describe, expect, it } from "vitest";

import { buildSlackManifest } from "./slackManifest";

describe("buildSlackManifest", () => {
  it("uses Slack's Agent messaging experience and current lifecycle events", () => {
    const manifest = buildSlackManifest("Release Agent");

    expect(manifest).toContain("  agent_view:\n    agent_description:");
    expect(manifest).not.toContain("assistant_view:");
    expect(manifest).toContain("      - app_home_opened");
    expect(manifest).toContain("      - app_context_changed");
    expect(manifest).toContain("      - message.im");
    expect(manifest).not.toContain("assistant_thread_started");
    expect(manifest).not.toContain("assistant_thread_context_changed");
  });

  it("requests the bot read scopes and optional user search scope used by agent tools", () => {
    const manifest = buildSlackManifest("Release Agent");

    expect(manifest).toContain("      - channels:history");
    expect(manifest).toContain("      - groups:history");
    expect(manifest).toContain("      - files:read");
    expect(manifest).toContain("      - users:read");
    expect(manifest).toContain("    user:\n      - search:read");
  });

  it("pins non-contextual suggested prompts to the Messages tab", () => {
    const manifest = buildSlackManifest("Release Agent");

    expect(manifest).toContain("    suggested_prompts:");
    expect(manifest).toContain("      - title: What needs my attention?");
    expect(manifest).toContain("      - title: Draft a reply");
    expect(manifest).toContain("      - title: Summarize a channel");
  });

  it("quotes and limits the Slack app name", () => {
    const rawName = '  Agent: "production" with a very long name  ';
    const quoted = JSON.stringify(rawName.trim().slice(0, 35));
    const manifest = buildSlackManifest(rawName);

    expect(manifest).toContain(`  name: ${quoted}`);
    expect(manifest).toContain(`    display_name: ${quoted}`);
  });
});
