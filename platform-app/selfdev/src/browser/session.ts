// UI session orchestration: fake backend + web dev server + headless Chromium.
//
// Boot sequence (startUiSession):
//   1. start the fake ConnectRPC backend on an ephemeral port
//   2. spawn the web Vite dev server with WEB_BACKEND_URL pointed at it
//   3. launch the system Chromium via playwright-core (no browser download)
// Per page (capturePage):
//   4. sign in through the real login form once, cache storageState
//   5. install a fixed clock, navigate, settle, run steps
//   6. write PNG + aria snapshot + console/pageerror/network log
//
// Chromium comes from the runtime image (Debian `chromium` in the default
// worker image); override with SELFDEV_CHROMIUM=/path/to/chrome.

import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, type Browser, type BrowserContext, type Page } from "playwright-core";
import type { Scenario } from "../scenario";
import { startFakeBackend, type FakeBackend } from "../server/fake-backend";
import { buildTauriSimScript } from "./tauri-sim";
import { runSteps, type Step } from "./steps";

const SELFDEV_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const APP_ROOT = path.resolve(SELFDEV_DIR, "..");
const WEB_DIR = path.join(APP_ROOT, "web");

// ---------------------------------------------------------------------------
// Chromium discovery
// ---------------------------------------------------------------------------

const CHROMIUM_NAMES = [
  "chromium",
  "chromium-browser",
  "google-chrome-stable",
  "google-chrome",
  "headless_shell",
];

export function findChromium(): string | null {
  const fromEnv = process.env.SELFDEV_CHROMIUM;
  if (fromEnv && existsSync(fromEnv)) return fromEnv;
  const dirs = [
    ...(process.env.PATH ?? "").split(path.delimiter).filter(Boolean),
    "/usr/bin",
    "/usr/local/bin",
    "/snap/bin",
  ];
  for (const name of CHROMIUM_NAMES) {
    for (const dir of dirs) {
      const candidate = path.join(dir, name);
      if (existsSync(candidate)) return candidate;
    }
  }
  return null;
}

export function launchChromium(executablePath: string): Promise<Browser> {
  return chromium.launch({
    executablePath,
    headless: true,
    args: [
      "--no-sandbox",
      "--disable-dev-shm-usage",
      "--disable-gpu",
      "--hide-scrollbars",
      "--force-color-profile=srgb",
      "--font-render-hinting=none",
    ],
  });
}

// ---------------------------------------------------------------------------
// Vite dev server
// ---------------------------------------------------------------------------

async function waitForHttp(url: string, timeoutMs: number, child?: ChildProcess): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown = null;
  while (Date.now() < deadline) {
    if (child && child.exitCode !== null) {
      throw new Error(`vite dev server exited early with code ${child.exitCode}`);
    }
    try {
      const res = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (res.status < 500) return;
    } catch (err) {
      lastError = err;
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`timed out waiting for ${url}: ${lastError instanceof Error ? lastError.message : lastError}`);
}

function resolveViteBin(): string {
  // vite 8 does not expose ./bin/vite.js through its package exports map, so
  // require.resolve("vite/bin/vite.js") throws. Resolve the package root via
  // package.json (exported) or the pnpm symlink, then join the bin path.
  const direct = path.join(WEB_DIR, "node_modules", "vite", "bin", "vite.js");
  if (existsSync(direct)) return direct;
  try {
    const require = createRequire(path.join(WEB_DIR, "package.json"));
    const pkg = require.resolve("vite/package.json");
    const bin = path.join(path.dirname(pkg), "bin", "vite.js");
    if (existsSync(bin)) return bin;
  } catch {
    // fall through
  }
  throw new Error(
    `could not locate vite under ${WEB_DIR} — run \`pnpm install\` at the workspace root first`,
  );
}

function startVite(uiPort: number, backendUrl: string): { child: ChildProcess; logs: string[] } {
  const viteBin = resolveViteBin();
  const logs: string[] = [];
  const child = spawn(
    process.execPath,
    [viteBin, "--port", String(uiPort), "--strictPort", "--logLevel", "warn"],
    {
      cwd: WEB_DIR,
      env: { ...process.env, WEB_BACKEND_URL: backendUrl, BROWSER: "none" },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  child.stdout?.on("data", (d: Buffer) => logs.push(d.toString()));
  child.stderr?.on("data", (d: Buffer) => logs.push(d.toString()));
  return { child, logs };
}

// ---------------------------------------------------------------------------
// Console / network recorder
// ---------------------------------------------------------------------------

interface Recorder {
  lines: string[];
  stop(): void;
}

function attachRecorder(page: Page): Recorder {
  const lines: string[] = [];
  let recording = true;
  const record = (line: string) => {
    if (recording) lines.push(line);
  };
  page.on("console", (msg) => {
    const type = msg.type();
    if (type === "error" || type === "warning") record(`[console.${type}] ${msg.text()}`);
  });
  page.on("pageerror", (err) => record(`[pageerror] ${err.message}`));
  page.on("requestfailed", (req) => {
    record(`[requestfailed] ${req.method()} ${req.url()} — ${req.failure()?.errorText ?? "unknown"}`);
  });
  page.on("response", (res) => {
    if (res.status() >= 400) record(`[http ${res.status()}] ${res.request().method()} ${res.url()}`);
  });
  return {
    lines,
    stop() {
      recording = false;
    },
  };
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

export interface PageOptions {
  route: string;
  theme?: "dark" | "light";
  viewport?: { width: number; height: number };
  tauriSim?: boolean;
  /** OS reported by the Tauri sim (macos|ios|linux|windows). */
  tauriPlatform?: "macos" | "ios" | "linux" | "windows";
  /** Skip the login flow (to screenshot the login page itself). */
  noAuth?: boolean;
  /** Selector that must be visible before the shot (default #main-content). */
  waitFor?: string;
  /** Extra settle delay after load, in ms. */
  settleMs?: number;
  fullPage?: boolean;
  steps?: Step[];
}

export interface CaptureResult {
  pngPath: string;
  ariaPath: string;
  consolePath: string;
  consoleLines: string[];
}

export interface UiSessionOptions {
  scenario: Scenario;
  /** Fake backend port; 0 (default) = ephemeral. */
  backendPort?: number;
  /** Vite dev server port (default 5199). */
  uiPort?: number;
  outDir?: string;
  viteStartupTimeoutMs?: number;
}

export class UiSession {
  readonly scenario: Scenario;
  readonly baseUrl: string;
  readonly backend: FakeBackend;
  readonly outDir: string;
  private readonly browser: Browser;
  private readonly vite: { child: ChildProcess; logs: string[] };
  private readonly authStates = new Map<string, unknown>();

  private constructor(args: {
    scenario: Scenario;
    baseUrl: string;
    backend: FakeBackend;
    browser: Browser;
    vite: { child: ChildProcess; logs: string[] };
    outDir: string;
  }) {
    this.scenario = args.scenario;
    this.baseUrl = args.baseUrl;
    this.backend = args.backend;
    this.browser = args.browser;
    this.vite = args.vite;
    this.outDir = args.outDir;
  }

  static async start(options: UiSessionOptions): Promise<UiSession> {
    const executablePath = findChromium();
    if (!executablePath) {
      throw new Error(
        "no Chromium found. Install the Debian `chromium` package (present in the default " +
          "worker runtime image) or set SELFDEV_CHROMIUM=/path/to/chrome.",
      );
    }

    const backend = await startFakeBackend(options.scenario, { port: options.backendPort ?? 0 });
    const uiPort = options.uiPort ?? 5199;
    const vite = startVite(uiPort, backend.url);
    const baseUrl = `http://localhost:${uiPort}`;
    try {
      await waitForHttp(baseUrl, options.viteStartupTimeoutMs ?? 90_000, vite.child);
    } catch (err) {
      vite.child.kill("SIGKILL");
      await backend.close();
      const tail = vite.logs.join("").split("\n").slice(-20).join("\n");
      throw new Error(`${err instanceof Error ? err.message : err}\n--- vite output ---\n${tail}`);
    }

    let browser: Browser;
    try {
      browser = await launchChromium(executablePath);
    } catch (err) {
      vite.child.kill("SIGKILL");
      await backend.close();
      throw err;
    }

    const outDir = options.outDir ?? path.join(SELFDEV_DIR, "out");
    mkdirSync(outDir, { recursive: true });

    return new UiSession({ scenario: options.scenario, baseUrl, backend, browser, vite, outDir });
  }

  private async newContext(opts: PageOptions): Promise<BrowserContext> {
    const theme = opts.theme ?? "dark";
    const authKey = opts.tauriSim ? "tauri" : "web";
    const storageState = opts.noAuth ? undefined : this.authStates.get(authKey);

    const context = await this.browser.newContext({
      viewport: opts.viewport ?? { width: 1440, height: 900 },
      reducedMotion: "reduce",
      colorScheme: theme,
      // Playwright accepts a previously captured storageState object.
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      storageState: storageState as any,
    });
    // Pin the app theme regardless of what storageState carried over.
    await context.addInitScript(
      `try { localStorage.setItem("theme", ${JSON.stringify(theme)}); } catch {}`,
    );
    if (opts.tauriSim) {
      await context.addInitScript(
        buildTauriSimScript({
          platform: opts.tauriPlatform ?? "macos",
          localCredentials: this.scenario.localCredentials,
        }),
      );
    }
    return context;
  }

  /** Signs in through the real login form; caches storageState per mode. */
  private async ensureAuth(page: Page, opts: PageOptions): Promise<void> {
    const authKey = opts.tauriSim ? "tauri" : "web";
    await page.goto(`${this.baseUrl}/`, { waitUntil: "domcontentloaded" });

    // Already signed in (cached storageState)?
    const shell = page.locator("#main-content");
    const username = page.locator("#username");
    await Promise.race([
      shell.waitFor({ state: "visible", timeout: 30_000 }),
      username.waitFor({ state: "visible", timeout: 30_000 }),
    ]);
    if (await shell.isVisible()) return;

    // Tauri sim boots unconfigured: connect to the dev-server origin first
    // (proxied to the fake backend), exactly like a desktop user would.
    const operatorUrl = page.locator("#gratefulagents-url");
    if (await operatorUrl.isVisible()) {
      await operatorUrl.fill(this.baseUrl);
      await page.getByRole("button", { name: /^(Connect|Reconnect)$/ }).click();
      await username.waitFor({ state: "visible", timeout: 15_000 });
    }

    await username.fill("selfdev");
    await page.locator("#password").fill("selfdev");
    await page.locator('button[type="submit"]').click();
    await shell.waitFor({ state: "visible", timeout: 30_000 });

    this.authStates.set(authKey, await page.context().storageState());
  }

  /** Navigates, settles, runs steps, and writes PNG + aria + console files. */
  async capturePage(opts: PageOptions, outBaseName: string): Promise<CaptureResult> {
    const context = await this.newContext(opts);
    try {
      const page = await context.newPage();
      const recorder = attachRecorder(page);
      await page.clock.install({ time: this.scenario.now });

      if (!opts.noAuth) await this.ensureAuth(page, opts);
      await page.goto(`${this.baseUrl}${opts.route}`, { waitUntil: "domcontentloaded" });

      const waitFor = opts.waitFor ?? (opts.noAuth ? "body" : "#main-content");
      await page
        .locator(waitFor)
        .first()
        .waitFor({ state: "visible", timeout: 30_000 })
        .catch(() => {
          recorder.lines.push(`[selfdev] wait-for selector never became visible: ${waitFor}`);
        });
      await page.evaluate(() => (document as Document & { fonts: FontFaceSet }).fonts.ready);
      if (opts.steps?.length) await runSteps(page, opts.steps, this.baseUrl);
      await page.waitForTimeout(opts.settleMs ?? 700);

      const pngPath = path.join(this.outDir, `${outBaseName}.png`);
      const ariaPath = path.join(this.outDir, `${outBaseName}.aria.yml`);
      const consolePath = path.join(this.outDir, `${outBaseName}.console.log`);

      await page.screenshot({ path: pngPath, fullPage: opts.fullPage ?? false });
      const aria = await page.locator("body").ariaSnapshot();
      recorder.stop();
      writeFileSync(ariaPath, `${aria}\n`);
      writeFileSync(
        consolePath,
        recorder.lines.length ? `${recorder.lines.join("\n")}\n` : "(no console errors, page errors, or failed requests)\n",
      );

      return { pngPath, ariaPath, consolePath, consoleLines: recorder.lines };
    } finally {
      await context.close();
    }
  }

  async close(): Promise<void> {
    await this.browser.close().catch(() => {});
    this.vite.child.kill("SIGTERM");
    const killTimer = setTimeout(() => this.vite.child.kill("SIGKILL"), 3000);
    await new Promise<void>((resolve) => {
      if (this.vite.child.exitCode !== null) return resolve();
      this.vite.child.once("exit", () => resolve());
    });
    clearTimeout(killTimer);
    await this.backend.close();
  }
}
