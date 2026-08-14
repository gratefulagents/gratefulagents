import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

export type ImmunefiTarget = {
  name: string;
  displayName: string;
  repoUrl: string;
  baseBranch: string;
  workflowRef: string;
  policyPackRef: string;
  securityProgramRef: string;
  provider: string;
  authMode: string;
  model: string;
  reasoningLevel: string;
  priority: number;
};

export function featuredImmunefiTargets(
  programs: readonly SecurityProgramResource[],
): ImmunefiTarget[] {
  return programs
    .filter((program) => program.scanTarget?.featured)
    .map((program) => ({
      name: program.scanTarget!.scanName,
      displayName: program.scanTarget!.displayName,
      repoUrl: program.scanTarget!.repositoryUrl,
      baseBranch: program.scanTarget!.baseBranch,
      workflowRef: program.scanTarget!.workflowRef,
      policyPackRef: program.scanTarget!.policyPackRef,
      securityProgramRef: program.name,
      provider: program.scanTarget!.provider || "openai",
      authMode: program.scanTarget!.authMode || "oauth",
      model: program.scanTarget!.model || "gpt-5.6-sol",
      reasoningLevel: program.scanTarget!.reasoningLevel || "max",
      priority: program.scanTarget!.priority,
    }))
    .sort(
      (left, right) =>
        left.priority - right.priority || left.displayName.localeCompare(right.displayName),
    );
}
