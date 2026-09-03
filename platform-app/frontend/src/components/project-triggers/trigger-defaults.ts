import { describeCron } from "@/components/project-triggers/connection-guides";

/**
 * Derived defaults for the trigger form so the user types as little as
 * possible: a name that reads like the trigger, and the browser's time zone.
 */

export function slugify(value: string, max = 40): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, max)
    .replace(/-+$/g, "");
}

type NameSource = {
  type: string;
  repository: string;
  channel: string;
  schedule: string;
  team: string;
  project: string;
};

/**
 * Suggest a DNS-label name from the trigger's own settings:
 * github → `gh-acme-payments`, slack → `slack-engineering` / `slack-mentions`,
 * cron → `weekdays-0900` / `every-hour` / `cron-…`, linear → `linear-eng`.
 */
export function suggestTriggerName(form: NameSource): string {
  switch (form.type) {
    case "github": {
      const [owner = "", repo = ""] = form.repository.trim().split("/", 2);
      const parts = [owner, repo].map((p) => slugify(p, 24)).filter(Boolean);
      return parts.length ? `gh-${parts.join("-")}` : "";
    }
    case "slack": {
      const channel = form.channel.trim().replace(/^#/, "");
      return channel ? `slack-${slugify(channel, 30)}` : "slack-mentions";
    }
    case "cron": {
      const human = describeCron(form.schedule);
      if (human) return slugify(human.replace(/:/g, "").replace(/\bat\b/g, " "), 40);
      const raw = form.schedule.trim();
      return raw ? `cron-${slugify(raw.replace(/\*/g, "any"), 30)}` : "";
    }
    case "linear": {
      const scope = form.project.trim() || form.team.trim();
      return scope ? `linear-${slugify(scope, 30)}` : "";
    }
    default:
      return "";
  }
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

const COMMON_ZONES = [
  "UTC",
  "America/Los_Angeles",
  "America/Denver",
  "America/Chicago",
  "America/New_York",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "Europe/Madrid",
  "Europe/Amsterdam",
  "Europe/Stockholm",
  "Europe/Kyiv",
  "Asia/Kolkata",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Asia/Shanghai",
  "Australia/Sydney",
];

export function timeZoneOptions(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  try {
    const all = intl.supportedValuesOf?.("timeZone");
    if (all?.length) return ["UTC", ...all.filter((z) => z !== "UTC")];
  } catch {
    // Fall back to the curated list below.
  }
  return COMMON_ZONES;
}
