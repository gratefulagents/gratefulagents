import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

export type ImmunefiTarget = {
  name: string;
  displayName: string;
  repoUrl: string;
  baseBranch: string;
  workflowRef: string;
  policyPackRef: string;
  securityProgramRef: string;
  priority: number;
};

export function importableImmunefiTargets(
  programs: readonly SecurityProgramResource[],
): ImmunefiTarget[] {
  return programs
    .flatMap((program) => {
      const targets = program.scanTargets?.length
        ? program.scanTargets
        : program.scanTarget
          ? [program.scanTarget]
          : [];
      return targets.map((target) => ({
        name: target.scanName,
        displayName: target.displayName,
        repoUrl: target.repositoryUrl,
        baseBranch: target.baseBranch || "main",
        workflowRef: target.workflowRef,
        policyPackRef: target.policyPackRef,
        securityProgramRef: program.name,
        priority: target.priority,
      }));
    })
    .sort(
      (left, right) =>
        left.priority - right.priority || left.displayName.localeCompare(right.displayName),
    );
}
