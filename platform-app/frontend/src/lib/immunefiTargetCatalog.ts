import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

export type ImmunefiTarget = {
  name: string;
  displayName: string;
  repoUrl: string;
  workflowRef: string;
  policyPackRef: string;
  securityProgramRef: string;
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
      workflowRef: program.scanTarget!.workflowRef,
      policyPackRef: program.scanTarget!.policyPackRef,
      securityProgramRef: program.name,
      priority: program.scanTarget!.priority,
    }))
    .sort(
      (left, right) =>
        left.priority - right.priority || left.displayName.localeCompare(right.displayName),
    );
}
