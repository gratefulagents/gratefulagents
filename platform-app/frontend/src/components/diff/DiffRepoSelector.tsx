import type { ReactNode } from "react";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { RepositoryInfo } from "@/rpc/platform/service_pb";

export type DiffRepoSelectorProps = {
  repositories: RepositoryInfo[];
  /** Path of the selected repository (RepositoryInfo.path). */
  value: string;
  onChange: (path: string) => void;
};

/**
 * DiffRepoSelector picks which workspace repository the diff view shows when a
 * run has more than one repo (primary clone plus attached/cloned extras).
 * Values are the sandbox paths reported by ListRepositories.
 */
export function DiffRepoSelector({ repositories, value, onChange }: DiffRepoSelectorProps): ReactNode {
  if (repositories.length < 2) {
    return null;
  }
  const primary = repositories.find((repo) => repo.isPrimary);
  const selected = value || primary?.path || repositories[0].path;

  return (
    <Select
      value={selected}
      onValueChange={(path) => {
        if (typeof path === "string" && path) onChange(path);
      }}
    >
      <SelectTrigger
        size="sm"
        aria-label="Repository to diff"
        className="h-7 max-w-56 gap-1.5 text-xs"
      >
        <SelectValue placeholder="Repository" />
      </SelectTrigger>
      <SelectContent>
        {repositories.map((repo) => (
          <SelectItem key={repo.path} value={repo.path} className="text-xs">
            <span className="truncate">{repo.name}</span>
            {repo.isPrimary && <span className="text-muted-foreground"> (primary)</span>}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
