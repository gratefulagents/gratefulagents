import type { RuntimeImageOption, RuntimeImageVersion } from "@/rpc/platform/service_pb";

export const CUSTOM_IMAGE_ID = "__custom__";

export type RuntimeImageSelection = {
  languageId: string;
  /** Version label within the language; "" when languageId is CUSTOM_IMAGE_ID. */
  version: string;
};

/**
 * Maps a raw image ref to the catalog coordinates that should appear selected:
 * empty → the catalog default language (at its default version), a known
 * catalog image → its language+version, anything else → the custom choice.
 */
export function selectionForImage(images: RuntimeImageOption[], image: string): RuntimeImageSelection {
  const trimmed = image.trim();
  if (trimmed === "") {
    const def =
      images.find((option) => option.isDefault) ??
      images.find((option) => option.versions.some((v) => v.image === ""));
    if (def) return { languageId: def.id, version: defaultVersion(def)?.version ?? "" };
    return { languageId: CUSTOM_IMAGE_ID, version: "" };
  }
  for (const option of images) {
    const match = option.versions.find((v) => v.image === trimmed);
    if (match) return { languageId: option.id, version: match.version };
  }
  return { languageId: CUSTOM_IMAGE_ID, version: "" };
}

/** Returns the image ref for a language+version pick ("" for platform default). */
export function imageForSelection(images: RuntimeImageOption[], selection: RuntimeImageSelection, customImage: string): string {
  if (selection.languageId === CUSTOM_IMAGE_ID) return customImage.trim();
  const option = images.find((o) => o.id === selection.languageId);
  if (!option) return customImage.trim();
  const version =
    option.versions.find((v) => v.version === selection.version) ?? defaultVersion(option);
  return version ? version.image : customImage.trim();
}

/** The version preselected for a language (flagged default, else first). */
export function defaultVersion(option: RuntimeImageOption): RuntimeImageVersion | undefined {
  return option.versions.find((v) => v.isDefault) ?? option.versions[0];
}
