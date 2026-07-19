import { GitPullRequest } from "lucide-react";

import { FlowSwitchRow, OptionRow } from "@/components/create-flow/create-flow";
import { Switch } from "@/components/ui/switch";

export function ProjectReviewLoopOption({
  id,
  disabled,
  modified,
  onDisabledChange,
}: {
  id: string;
  disabled: boolean;
  modified: boolean;
  onDisabledChange: (disabled: boolean) => void;
}) {
  return (
    <OptionRow
      icon={GitPullRequest}
      title="PR review loop"
      summary={disabled ? "Disabled" : "Enabled"}
      modified={modified}
    >
      <FlowSwitchRow
        id={id}
        label="Disable autonomous PR review loop"
        hint="New runs inherit this policy for every PR they open, including PRs in additional repositories."
        control={
          <Switch
            id={id}
            checked={disabled}
            onCheckedChange={onDisabledChange}
          />
        }
      />
    </OptionRow>
  );
}
