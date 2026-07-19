import type { Scenario } from "../scenario";
import { defaultScenario } from "./default";
import { emptyScenario } from "./empty";
import { errorScenario } from "./error";

export const scenarios: Record<string, Scenario> = {
  default: defaultScenario,
  empty: emptyScenario,
  error: errorScenario,
};

export function getScenario(name: string): Scenario {
  const scenario = scenarios[name];
  if (!scenario) {
    const known = Object.keys(scenarios).join(", ");
    throw new Error(`unknown scenario "${name}" (known: ${known})`);
  }
  return scenario;
}
