import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { RuntimeImageOptionSchema } from "@/rpc/platform/service_pb";
import { CUSTOM_IMAGE_ID, defaultVersion, imageForSelection, selectionForImage } from "./runtimeImages";

const catalog = [
  create(RuntimeImageOptionSchema, {
    id: "default",
    label: "Default",
    isDefault: true,
    versions: [{ version: "latest", image: "", isDefault: true }],
  }),
  create(RuntimeImageOptionSchema, {
    id: "ruby",
    label: "Ruby",
    versions: [
      { version: "3.4", image: "docker.io/library/ruby:3.4", isDefault: true },
      { version: "3.3", image: "docker.io/library/ruby:3.3" },
    ],
  }),
  create(RuntimeImageOptionSchema, {
    id: "node",
    label: "Node.js",
    versions: [
      { version: "24", image: "docker.io/library/node:24", isDefault: true },
      { version: "22", image: "docker.io/library/node:22" },
    ],
  }),
];

describe("selectionForImage", () => {
  it("maps empty image to the default language at its default version", () => {
    expect(selectionForImage(catalog, "")).toEqual({ languageId: "default", version: "latest" });
    expect(selectionForImage(catalog, "   ")).toEqual({ languageId: "default", version: "latest" });
  });

  it("maps a known image to its language and version", () => {
    expect(selectionForImage(catalog, "docker.io/library/ruby:3.3")).toEqual({
      languageId: "ruby",
      version: "3.3",
    });
    expect(selectionForImage(catalog, "  docker.io/library/node:24  ")).toEqual({
      languageId: "node",
      version: "24",
    });
  });

  it("maps an unknown image to custom", () => {
    expect(selectionForImage(catalog, "ghcr.io/acme/monorepo:dev")).toEqual({
      languageId: CUSTOM_IMAGE_ID,
      version: "",
    });
  });

  it("falls back to the empty-image option when none is flagged default", () => {
    const noFlag = catalog.map((option) =>
      create(RuntimeImageOptionSchema, { ...option, isDefault: false }),
    );
    expect(selectionForImage(noFlag, "").languageId).toBe("default");
  });

  it("handles an empty catalog", () => {
    expect(selectionForImage([], "").languageId).toBe(CUSTOM_IMAGE_ID);
    expect(selectionForImage([], "anything").languageId).toBe(CUSTOM_IMAGE_ID);
  });
});

describe("imageForSelection", () => {
  it("returns the catalog image for a language+version", () => {
    expect(imageForSelection(catalog, { languageId: "ruby", version: "3.3" }, "")).toBe(
      "docker.io/library/ruby:3.3",
    );
  });

  it("falls back to the language default version for an unknown version", () => {
    expect(imageForSelection(catalog, { languageId: "ruby", version: "9.9" }, "")).toBe(
      "docker.io/library/ruby:3.4",
    );
  });

  it("returns empty for the default language", () => {
    expect(imageForSelection(catalog, { languageId: "default", version: "latest" }, "whatever")).toBe("");
  });

  it("returns the trimmed custom image for the custom option", () => {
    expect(imageForSelection(catalog, { languageId: CUSTOM_IMAGE_ID, version: "" }, "  ghcr.io/acme/img:1  ")).toBe(
      "ghcr.io/acme/img:1",
    );
  });
});

describe("defaultVersion", () => {
  it("prefers the flagged default", () => {
    expect(defaultVersion(catalog[1])?.version).toBe("3.4");
  });

  it("falls back to the first version when none flagged", () => {
    const noFlag = create(RuntimeImageOptionSchema, {
      id: "x",
      label: "X",
      versions: [
        { version: "1", image: "x:1" },
        { version: "2", image: "x:2" },
      ],
    });
    expect(defaultVersion(noFlag)?.version).toBe("1");
  });
});
