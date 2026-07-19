import { Plus, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/**
 * Controlled editor for a list of git repository URLs: one text input per
 * repository with add/remove controls. Used for the additional repositories
 * cloned into a run's sandbox next to the primary repository.
 */
export function RepoUrlListInput({
  value,
  onChange,
  idPrefix,
  placeholder = "https://github.com/org/other-repo",
}: {
  value: string[];
  onChange: (urls: string[]) => void;
  idPrefix: string;
  placeholder?: string;
}) {
  function updateAt(index: number, url: string) {
    onChange(value.map((existing, i) => (i === index ? url : existing)));
  }
  function removeAt(index: number) {
    onChange(value.filter((_, i) => i !== index));
  }
  return (
    <div className="space-y-2">
      {value.map((url, index) => (
        <div key={index} className="flex items-center gap-2">
          <Input
            id={`${idPrefix}-${index}`}
            value={url}
            onChange={(event) => updateAt(index, event.target.value)}
            placeholder={placeholder}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={`Remove additional repository ${index + 1}`}
            onClick={() => removeAt(index)}
          >
            <X className="size-4" />
          </Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" onClick={() => onChange([...value, ""])}>
        <Plus className="size-4" />
        Add repository
      </Button>
    </div>
  );
}
