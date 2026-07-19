// Interaction steps: a tiny JSON schema for driving the page before a
// screenshot (open a dialog, hover a row, type into the composer, …).
// Selectors are Playwright selector strings (CSS, text=…, role=…, #id, …).

import type { Page } from "playwright-core";

export type Step =
  | { action: "click"; selector: string }
  | { action: "dblclick"; selector: string }
  | { action: "fill"; selector: string; value: string }
  | { action: "type"; selector: string; value: string }
  | { action: "press"; key: string; selector?: string }
  | { action: "hover"; selector: string }
  | { action: "focus"; selector: string }
  | { action: "scrollIntoView"; selector: string }
  | { action: "waitFor"; selector: string; state?: "attached" | "visible" | "hidden" }
  | { action: "wait"; ms: number }
  | { action: "goto"; route: string };

const KNOWN_ACTIONS = new Set([
  "click",
  "dblclick",
  "fill",
  "type",
  "press",
  "hover",
  "focus",
  "scrollIntoView",
  "waitFor",
  "wait",
  "goto",
]);

export function parseSteps(json: string): Step[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch (err) {
    throw new Error(`steps: invalid JSON: ${err instanceof Error ? err.message : String(err)}`);
  }
  if (!Array.isArray(parsed)) throw new Error("steps: expected a JSON array of steps");

  return parsed.map((raw, i) => {
    if (typeof raw !== "object" || raw === null) throw new Error(`steps[${i}]: expected an object`);
    const step = raw as Record<string, unknown>;
    const action = step.action;
    if (typeof action !== "string" || !KNOWN_ACTIONS.has(action)) {
      throw new Error(`steps[${i}]: unknown action ${JSON.stringify(action)} (known: ${[...KNOWN_ACTIONS].join(", ")})`);
    }
    const needString = (field: string) => {
      if (typeof step[field] !== "string" || (step[field] as string).length === 0) {
        throw new Error(`steps[${i}] (${action}): missing string field "${field}"`);
      }
    };
    switch (action) {
      case "click":
      case "dblclick":
      case "hover":
      case "focus":
      case "scrollIntoView":
      case "waitFor":
        needString("selector");
        break;
      case "fill":
      case "type":
        needString("selector");
        if (typeof step.value !== "string") throw new Error(`steps[${i}] (${action}): missing string field "value"`);
        break;
      case "press":
        needString("key");
        break;
      case "wait":
        if (typeof step.ms !== "number" || step.ms < 0) throw new Error(`steps[${i}] (wait): missing numeric field "ms"`);
        break;
      case "goto":
        needString("route");
        break;
    }
    return step as unknown as Step;
  });
}

export async function runSteps(page: Page, steps: Step[], baseUrl: string): Promise<void> {
  for (const step of steps) {
    switch (step.action) {
      case "click":
        await page.locator(step.selector).first().click();
        break;
      case "dblclick":
        await page.locator(step.selector).first().dblclick();
        break;
      case "fill":
        await page.locator(step.selector).first().fill(step.value);
        break;
      case "type":
        await page.locator(step.selector).first().pressSequentially(step.value);
        break;
      case "press":
        if (step.selector) await page.locator(step.selector).first().press(step.key);
        else await page.keyboard.press(step.key);
        break;
      case "hover":
        await page.locator(step.selector).first().hover();
        break;
      case "focus":
        await page.locator(step.selector).first().focus();
        break;
      case "scrollIntoView":
        await page.locator(step.selector).first().scrollIntoViewIfNeeded();
        break;
      case "waitFor":
        await page.locator(step.selector).first().waitFor({ state: step.state ?? "visible" });
        break;
      case "wait":
        await page.waitForTimeout(step.ms);
        break;
      case "goto":
        await page.goto(`${baseUrl}${step.route}`, { waitUntil: "domcontentloaded" });
        break;
    }
  }
}
