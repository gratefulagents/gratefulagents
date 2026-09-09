import { useEffect, useId, useState } from "react";

import { Input } from "@/components/ui/input";
import { client } from "@/lib/client";

export function BranchPicker({
  id,
  repoUrl,
  namespace = "",
  value,
  onChange,
  placeholder = "main",
}: {
  id?: string;
  repoUrl: string;
  namespace?: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
}) {
  const listId = useId();
  const [active, setActive] = useState(false);
  const [catalog, setCatalog] = useState<{
    key: string;
    branches: string[];
    loading: boolean;
    error: boolean;
  } | null>(null);
  const url = repoUrl.trim().replace(/\/+$/, "");
  const supported = /^https:\/\/github\.com\/[^/?#]+\/[^/?#]+$/.test(url);
  const key = JSON.stringify([namespace, url]);
  const current = catalog?.key === key ? catalog : null;

  useEffect(() => {
    if (!active || !supported) return;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      setCatalog({ key, branches: [], loading: true, error: false });
      void (async () => {
        const branches = new Set<string>();
        let page = 0;
        try {
          do {
            const response = await client.listGitHubBranches(
              { repoUrl: url, namespace, page },
              { signal: controller.signal },
            );
            if (controller.signal.aborted) return;
            response.branches.forEach((branch) => branches.add(branch));
            page = response.nextPage;
            setCatalog({ key, branches: [...branches], loading: page !== 0, error: false });
          } while (page !== 0);
        } catch {
          if (!controller.signal.aborted) {
            setCatalog({ key, branches: [...branches], loading: false, error: true });
          }
        }
      })();
    }, 250);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [active, supported, key, url, namespace]);

  return (
    <div className="space-y-1.5">
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onFocus={() => setActive(true)}
        list={listId}
        aria-describedby={`${listId}-status`}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
      />
      <datalist id={listId}>
        {supported && current?.branches.map((branch) => <option key={branch} value={branch} />)}
      </datalist>
      <p id={`${listId}-status`} role="status" className="text-xs text-muted-foreground">
        {current?.error
          ? "Could not load branches. You can still type any branch name."
          : active && supported && (!current || current.loading)
            ? "Loading branches… You can still type any branch name."
            : "Choose a GitHub branch or type any branch name."}
      </p>
    </div>
  );
}
