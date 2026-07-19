import { useMemo, useState } from "react";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useRuntimeImages } from "@/hooks/useRuntimeImages";
import { CUSTOM_IMAGE_ID, defaultVersion, imageForSelection, selectionForImage } from "@/lib/runtimeImages";

/**
 * Language-first worker image picker with per-language versions. The value is
 * the raw image ref stored on the project/agent/run ("" = platform default);
 * the picker renders it as language + version choices from the server catalog,
 * with a custom-image escape hatch.
 */
export function RuntimeImagePicker({
  id,
  value,
  onChange,
  disabled,
}: {
  id?: string;
  value: string;
  onChange: (image: string) => void;
  disabled?: boolean;
}) {
  const { images, loading } = useRuntimeImages();
  // Tracks an explicit "Custom image…" pick so the select doesn't snap back to
  // a language while the user is still typing a ref that happens to match one.
  const [customPicked, setCustomPicked] = useState(false);

  const selection = useMemo(() => {
    if (customPicked) return { languageId: CUSTOM_IMAGE_ID, version: "" };
    return selectionForImage(images, value);
  }, [customPicked, images, value]);

  const selectedOption = images.find((option) => option.id === selection.languageId);
  const isCustom = selection.languageId === CUSTOM_IMAGE_ID;
  const showVersions = !isCustom && (selectedOption?.versions.length ?? 0) > 1;

  if (loading && images.length === 0) {
    return <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder="Default worker image" disabled={disabled} />;
  }

  return (
    <div className="space-y-2">
      <div className="flex gap-2">
        <Select
          value={selection.languageId}
          disabled={disabled}
          onValueChange={(next) => {
            if (!next) return;
            if (next === CUSTOM_IMAGE_ID) {
              setCustomPicked(true);
              return;
            }
            setCustomPicked(false);
            const option = images.find((o) => o.id === next);
            const version = option ? defaultVersion(option) : undefined;
            onChange(
              imageForSelection(images, { languageId: next, version: version?.version ?? "" }, value),
            );
          }}
        >
          <SelectTrigger id={id} className={showVersions ? "flex-1" : "w-full"}>
            <SelectValue placeholder="Choose a language" />
          </SelectTrigger>
          <SelectContent>
            {images.map((option) => (
              <SelectItem key={option.id} value={option.id}>
                {option.label}
              </SelectItem>
            ))}
            <SelectItem value={CUSTOM_IMAGE_ID}>Custom image…</SelectItem>
          </SelectContent>
        </Select>
        {showVersions && selectedOption ? (
          <Select
            value={selection.version}
            disabled={disabled}
            onValueChange={(next) => {
              if (!next) return;
              onChange(
                imageForSelection(images, { languageId: selectedOption.id, version: next }, value),
              );
            }}
          >
            <SelectTrigger aria-label="Version" className="w-28 shrink-0">
              <SelectValue placeholder="Version" />
            </SelectTrigger>
            <SelectContent>
              {selectedOption.versions.map((version) => (
                <SelectItem key={version.version} value={version.version}>
                  {version.version}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : null}
      </div>
      {isCustom ? (
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="registry/repo:tag"
          disabled={disabled}
        />
      ) : null}
      <p className="text-xs text-muted-foreground">
        {isCustom
          ? "Agent tools are injected automatically; musl/alpine images are not supported."
          : (selectedOption?.description ?? "Agent tools are injected automatically.")}
      </p>
    </div>
  );
}
