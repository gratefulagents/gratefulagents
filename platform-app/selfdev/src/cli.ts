#!/usr/bin/env tsx
// selfdev CLI — run, drive, and screenshot the gratefulagents UI without a cluster.
//
//   pnpm --filter selfdev cli <command> [flags]
//
// Commands:
//   doctor                       check chromium/vite/node prerequisites
//   fake-server [--port 8090]    run only the fake ConnectRPC backend
//   serve                        fake backend + web dev server (interactive)
//   screenshot --route /path     capture one route
//   snap-all                     capture every scenario route (smoke pass)
//
// Shared flags:
//   --scenario default|empty|error   fixture scenario (default: default)
//   --theme dark|light|both          app theme (default: dark; snap-all: both)
//   --viewport WxH                   viewport pixels (default: 1440x900)
//   --tauri-sim                      fake __TAURI_INTERNALS__ (desktop UI)
//   --platform macos|ios|linux       OS reported by the Tauri sim
//   --full-page                      full-page screenshot
//   --steps file.json                interaction steps before the shot
//   --wait-for selector              readiness selector (default #main-content)
//   --settle ms                      extra settle delay (default 700)
//   --no-auth                        skip login (shoot the login page itself)
//   --out dir                        output dir (default selfdev/out)

import { existsSync, readFileSync } from "node:fs";
import { parseArgs } from "node:util";
import { getScenario, scenarios } from "./fixtures/index";
import { startFakeBackend } from "./server/fake-backend";
import { UiSession, findChromium, launchChromium, type PageOptions } from "./browser/session";
import { parseSteps, type Step } from "./browser/steps";

function fail(message: string): never {
  console.error(`selfdev: ${message}`);
  process.exit(1);
}

function parseViewport(value: string): { width: number; height: number } {
  const m = /^(\d+)x(\d+)$/.exec(value);
  if (!m) fail(`--viewport must look like 1440x900 (got "${value}")`);
  return { width: Number(m[1]), height: Number(m[2]) };
}

function slugForRoute(route: string): string {
  if (route === "/") return "home";
  return route.replace(/^\//, "").replace(/[^a-zA-Z0-9]+/g, "-").replace(/-+$/, "");
}

function outName(slug: string, scenario: string, theme: string, tauriSim: boolean): string {
  return `${slug}--${scenario}--${theme}${tauriSim ? "--tauri" : ""}`;
}

interface CommonFlags {
  scenario: string;
  themes: Array<"dark" | "light">;
  viewport: { width: number; height: number };
  tauriSim: boolean;
  tauriPlatform: "macos" | "ios" | "linux" | "windows";
  fullPage: boolean;
  steps: Step[] | undefined;
  waitFor: string | undefined;
  settleMs: number | undefined;
  noAuth: boolean;
  out: string | undefined;
  uiPort: number | undefined;
  backendPort: number | undefined;
}

function parseCommon(args: string[], defaults: { theme: string }): CommonFlags & { positionals: string[]; route?: string } {
  const { values, positionals } = parseArgs({
    args,
    allowPositionals: true,
    options: {
      route: { type: "string" },
      scenario: { type: "string", default: "default" },
      theme: { type: "string", default: defaults.theme },
      viewport: { type: "string", default: "1440x900" },
      "tauri-sim": { type: "boolean", default: false },
      platform: { type: "string", default: "macos" },
      "full-page": { type: "boolean", default: false },
      steps: { type: "string" },
      "wait-for": { type: "string" },
      settle: { type: "string" },
      "no-auth": { type: "boolean", default: false },
      out: { type: "string" },
      "ui-port": { type: "string" },
      "backend-port": { type: "string" },
      port: { type: "string" },
    },
  });

  const theme = values.theme as string;
  if (!["dark", "light", "both"].includes(theme)) fail(`--theme must be dark, light, or both (got "${theme}")`);
  const platform = values.platform as string;
  if (!["macos", "ios", "linux", "windows"].includes(platform)) {
    fail(`--platform must be macos, ios, linux, or windows (got "${platform}")`);
  }

  let steps: Step[] | undefined;
  if (values.steps) {
    steps = parseSteps(readFileSync(values.steps as string, "utf8"));
  }

  return {
    positionals,
    route: values.route as string | undefined,
    scenario: values.scenario as string,
    themes: theme === "both" ? ["dark", "light"] : [theme as "dark" | "light"],
    viewport: parseViewport(values.viewport as string),
    tauriSim: values["tauri-sim"] as boolean,
    tauriPlatform: platform as CommonFlags["tauriPlatform"],
    fullPage: values["full-page"] as boolean,
    steps,
    waitFor: values["wait-for"] as string | undefined,
    settleMs: values.settle ? Number(values.settle) : undefined,
    noAuth: values["no-auth"] as boolean,
    out: values.out as string | undefined,
    uiPort: values["ui-port"] ? Number(values["ui-port"]) : undefined,
    backendPort: values["backend-port"]
      ? Number(values["backend-port"])
      : values.port
        ? Number(values.port)
        : undefined,
  };
}

async function withSession<T>(flags: CommonFlags, fn: (session: UiSession) => Promise<T>): Promise<T> {
  const session = await UiSession.start({
    scenario: getScenario(flags.scenario),
    outDir: flags.out,
    uiPort: flags.uiPort,
    backendPort: flags.backendPort ?? 0,
  });
  try {
    return await fn(session);
  } finally {
    await session.close();
  }
}

function pageOptions(flags: CommonFlags, route: string, theme: "dark" | "light"): PageOptions {
  return {
    route,
    theme,
    viewport: flags.viewport,
    tauriSim: flags.tauriSim,
    tauriPlatform: flags.tauriPlatform,
    fullPage: flags.fullPage,
    steps: flags.steps,
    waitFor: flags.waitFor,
    settleMs: flags.settleMs,
    noAuth: flags.noAuth,
  };
}

async function cmdScreenshot(args: string[]): Promise<void> {
  const flags = parseCommon(args, { theme: "dark" });
  const route = flags.route ?? flags.positionals[0];
  if (!route || !route.startsWith("/")) fail("screenshot requires --route /some/path");

  await withSession(flags, async (session) => {
    for (const theme of flags.themes) {
      const name = outName(slugForRoute(route), flags.scenario, theme, flags.tauriSim);
      const result = await session.capturePage(pageOptions(flags, route, theme), name);
      console.log(result.pngPath);
      console.log(result.ariaPath);
      console.log(result.consolePath);
      if (result.consoleLines.length) {
        console.log(`  (${result.consoleLines.length} console/network finding(s) — see log)`);
      }
    }
  });
}

async function cmdSnapAll(args: string[]): Promise<void> {
  const flags = parseCommon(args, { theme: "both" });
  const scenario = getScenario(flags.scenario);
  let findings = 0;

  await withSession(flags, async (session) => {
    for (const route of scenario.routes) {
      for (const theme of flags.themes) {
        const name = outName(route.name, flags.scenario, theme, flags.tauriSim);
        try {
          const result = await session.capturePage(pageOptions(flags, route.path, theme), name);
          findings += result.consoleLines.length;
          console.log(`✓ ${name} (${result.consoleLines.length} finding(s))`);
        } catch (err) {
          findings += 1;
          console.log(`✗ ${name}: ${err instanceof Error ? err.message : err}`);
        }
      }
    }
    console.log(`\nwrote output to ${session.outDir}`);
  });
  console.log(findings ? `${findings} console/network finding(s) across all shots` : "no console findings");
}

async function cmdFakeServer(args: string[]): Promise<void> {
  const flags = parseCommon(args, { theme: "dark" });
  const backend = await startFakeBackend(getScenario(flags.scenario), {
    port: flags.backendPort ?? 8090,
  });
  console.log(`selfdev fake backend (scenario: ${flags.scenario}) listening on ${backend.url}`);
  console.log("serving platform.v1.PlatformService, auth.v1.AuthService, GET /api/config");
  console.log("any username/password signs in. Ctrl-C to stop.");
  await new Promise<void>((resolve) => {
    process.on("SIGINT", () => resolve());
    process.on("SIGTERM", () => resolve());
  });
  await backend.close();
}

async function cmdServe(args: string[]): Promise<void> {
  const flags = parseCommon(args, { theme: "dark" });
  const session = await UiSession.start({
    scenario: getScenario(flags.scenario),
    outDir: flags.out,
    uiPort: flags.uiPort,
    backendPort: flags.backendPort ?? 0,
  });
  console.log(`selfdev serve (scenario: ${flags.scenario})`);
  console.log(`  ui:      ${session.baseUrl}`);
  console.log(`  backend: ${session.backend.url}`);
  console.log("sign in with any username/password. Ctrl-C to stop.");
  await new Promise<void>((resolve) => {
    process.on("SIGINT", () => resolve());
    process.on("SIGTERM", () => resolve());
  });
  await session.close();
}

async function cmdDoctor(): Promise<void> {
  console.log(`node: ${process.version}`);
  const chromiumPath = findChromium();
  console.log(`chromium: ${chromiumPath ?? "NOT FOUND — install Debian chromium or set SELFDEV_CHROMIUM"}`);

  let chromiumReady = false;
  if (chromiumPath) {
    try {
      const browser = await launchChromium(chromiumPath);
      await browser.close();
      chromiumReady = true;
      console.log("chromium launch: ok");
    } catch (err) {
      console.error(`chromium launch: FAILED\n${err instanceof Error ? (err.stack ?? err.message) : err}`);
      if (!existsSync("/proc/self/exe")) {
        console.error(
          "hint: /proc is unavailable in the command sandbox. Enable " +
            "RuntimeProfile spec.sandbox.enablePrivateProcfs on a cluster that supports pod user namespaces.",
        );
      }
    }
  }

  console.log(`scenarios: ${Object.keys(scenarios).join(", ")}`);
  const backend = await startFakeBackend(getScenario("default"), { port: 0 });
  try {
    const res = await fetch(`${backend.url}/api/config`);
    console.log(`fake backend: ok (${backend.url}/api/config → ${res.status})`);
  } finally {
    await backend.close();
  }

  if (!chromiumReady) {
    console.log("\nscreenshots unavailable in this environment; fake-server/serve still work.");
    process.exitCode = 2;
  }
}

async function main(): Promise<void> {
  const [command, ...rest] = process.argv.slice(2);
  switch (command) {
    case "screenshot":
      return cmdScreenshot(rest);
    case "snap-all":
      return cmdSnapAll(rest);
    case "fake-server":
      return cmdFakeServer(rest);
    case "serve":
      return cmdServe(rest);
    case "doctor":
      return cmdDoctor();
    default:
      fail(`unknown command "${command ?? ""}" (expected: screenshot, snap-all, fake-server, serve, doctor)`);
  }
}

main().catch((err) => {
  console.error(err instanceof Error ? (err.stack ?? err.message) : err);
  process.exit(1);
});
