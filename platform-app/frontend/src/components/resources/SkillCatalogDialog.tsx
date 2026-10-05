import { useEffect, useRef, useState } from "react";
import { ExternalLink, Search } from "lucide-react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft } from "@/lib/status";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toaster";

interface CatalogSkill {
  source: string;
  skillId: string;
  name: string;
  installs: bigint;
  isOfficial: boolean;
  catalogUrl: string;
}

/**
 * Browse-and-install dialog for the skills.sh catalog. Stays open after an
 * install so several skills can be added in one sitting; the parent reloads
 * its list on each install.
 */
export function SkillCatalogDialog({
  installed,
  onClose,
  onInstalled,
}: {
  installed: Array<{ catalogSource: string; catalogSkillId: string }>;
  onClose: () => void;
  onInstalled: () => void;
}) {
  const [skills, setSkills] = useState<CatalogSkill[]>([]);
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [installing, setInstalling] = useState<string | null>(null);
  const requestGeneration = useRef(0);

  useEffect(() => {
    let cancelled = false;
    const generation = ++requestGeneration.current;
    const trimmed = query.trim();
    const requestQuery = trimmed.length >= 2 ? trimmed : "";
    const timer = window.setTimeout(() => {
      setLoading(true);
      void client
        .listSkillCatalog({ query: requestQuery, page: 0 })
        .then((resp) => {
          if (cancelled || generation !== requestGeneration.current) return;
          setSkills((resp.skills ?? []) as unknown as CatalogSkill[]);
          setPage(0);
          setHasMore(resp.hasMore);
        })
        .catch((err: unknown) => {
          if (!cancelled && generation === requestGeneration.current) {
            toast.error(err instanceof Error ? err.message : "Failed to load skills.sh");
          }
        })
        .finally(() => {
          if (!cancelled && generation === requestGeneration.current) setLoading(false);
        });
    }, requestQuery ? 250 : 0);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [query]);

  async function loadMore() {
    if (loading || !hasMore || query.trim().length >= 2) return;
    const generation = ++requestGeneration.current;
    setLoading(true);
    try {
      const nextPage = page + 1;
      const resp = await client.listSkillCatalog({ query: "", page: nextPage });
      if (generation !== requestGeneration.current) return;
      setSkills((current) => {
        const byCoordinate = new Map(current.map((skill) => [`${skill.source}/${skill.skillId}`, skill]));
        for (const skill of (resp.skills ?? []) as unknown as CatalogSkill[]) {
          byCoordinate.set(`${skill.source}/${skill.skillId}`, skill);
        }
        return [...byCoordinate.values()];
      });
      setPage(nextPage);
      setHasMore(resp.hasMore);
    } catch (err) {
      if (generation === requestGeneration.current) {
        toast.error(err instanceof Error ? err.message : "Failed to load more skills");
      }
    } finally {
      if (generation === requestGeneration.current) setLoading(false);
    }
  }

  async function install(skill: CatalogSkill) {
    const key = `${skill.source}/${skill.skillId}`;
    setInstalling(key);
    try {
      await client.installSkillFromCatalog({ source: skill.source, skillId: skill.skillId });
      toast.success(`Installed ${skill.name || skill.skillId}`);
      onInstalled();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `Failed to install ${skill.name || skill.skillId}`);
    } finally {
      setInstalling(null);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex w-full max-w-[calc(100%-1rem)] flex-col gap-0 overflow-hidden p-0 max-h-[88vh] sm:max-w-xl" showCloseButton>
        <DialogHeader className="space-y-1 border-b px-6 py-5 pr-12">
          <DialogTitle className="text-base">Install from skills.sh</DialogTitle>
          <DialogDescription className="text-[12.5px]">
            Choose a skill; it is installed as a Skill resource in your namespace.
          </DialogDescription>
        </DialogHeader>
        <div className="border-b px-6 py-3">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search skills.sh"
              aria-label="Search skills.sh"
              className="pl-8"
              autoFocus
            />
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-3">
          <ul role="list" className="space-y-1.5">
            {skills.map((skill) => {
              const key = `${skill.source}/${skill.skillId}`;
              const alreadyInstalled = installed.some(
                (current) => current.catalogSource === skill.source && current.catalogSkillId === skill.skillId,
              );
              const label = skill.name || skill.skillId;
              return (
                <li key={key} className="flex items-center justify-between gap-3 rounded-lg border border-border/60 px-3 py-2">
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-1.5 text-[12.5px] font-medium">
                      <span className="truncate">{label}</span>
                      {skill.isOfficial && (
                        <span className={cn("inline-flex h-[16px] items-center rounded-full px-1.5 text-[9.5px] font-medium uppercase", toneSoft.info)}>
                          Official
                        </span>
                      )}
                    </div>
                    <div className="flex min-w-0 items-center gap-2 text-[11px] text-muted-foreground">
                      <span className="truncate font-mono">{skill.source}</span>
                      <span className="shrink-0 tabular-nums">{skill.installs.toLocaleString()} installs</span>
                      <a
                        href={skill.catalogUrl}
                        target="_blank"
                        rel="noreferrer"
                        aria-label={`Open ${label} on skills.sh`}
                        className="shrink-0 hover:text-foreground"
                      >
                        <ExternalLink className="size-3" />
                      </a>
                    </div>
                  </div>
                  <Button
                    size="sm"
                    variant={alreadyInstalled ? "ghost" : "outline"}
                    disabled={alreadyInstalled || installing !== null}
                    onClick={() => void install(skill)}
                  >
                    {alreadyInstalled ? "Installed" : installing === key ? "Installing…" : "Install"}
                  </Button>
                </li>
              );
            })}
          </ul>
          {!loading && skills.length === 0 && (
            <p className="py-8 text-center text-[12px] text-muted-foreground">No matching skills found.</p>
          )}
          {loading && (
            <p className="py-3 text-center text-[12px] text-muted-foreground" role="status">
              Loading skills…
            </p>
          )}
        </div>
        {hasMore && query.trim().length < 2 && (
          <div className="border-t px-6 py-3">
            <Button size="sm" variant="outline" disabled={loading} onClick={() => void loadMore()}>
              Load more
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
