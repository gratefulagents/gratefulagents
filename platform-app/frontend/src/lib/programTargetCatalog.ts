import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

export type ProgramScanTarget = {
  name: string;
  displayName: string;
  repoUrl: string;
  targetUrl: string;
  baseBranch: string;
  workflowRef: string;
  policyPackRef: string;
  securityProgramRef: string;
  priority: number;
  parameterValues: Record<string, string>;
};

export function importableProgramTargets(
  programs: readonly SecurityProgramResource[],
): ProgramScanTarget[] {
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
        targetUrl: target.targetUrl,
        baseBranch: target.repositoryUrl ? target.baseBranch || "main" : "",
        workflowRef: target.workflowRef,
        policyPackRef: target.policyPackRef,
        securityProgramRef: program.name,
        priority: target.priority,
        parameterValues: { ...target.parameterValues },
      }));
    })
    .sort(
      (left, right) =>
        left.priority - right.priority || left.displayName.localeCompare(right.displayName),
    );
}
