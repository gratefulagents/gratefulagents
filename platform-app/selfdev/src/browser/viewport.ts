export const VIEWPORT_PRESETS = {
  desktop: { width: 1440, height: 900 },
  mobile: { width: 390, height: 844 },
  "ipad-portrait": { width: 820, height: 1180 },
  "ipad-landscape": { width: 1180, height: 820 },
} as const;

export type ViewportPreset = keyof typeof VIEWPORT_PRESETS;

export interface NamedViewport {
  name: string;
  size: { width: number; height: number };
}

const RESPONSIVE_PRESETS: ViewportPreset[] = [
  "desktop",
  "mobile",
  "ipad-portrait",
  "ipad-landscape",
];

export function parseViewports(value: string): NamedViewport[] {
  const names = value === "all" ? RESPONSIVE_PRESETS : value.split(",");
  if (names.some((name) => name.length === 0)) {
    throw new Error("viewport list cannot contain an empty value");
  }

  return names.map((name) => {
    if (name in VIEWPORT_PRESETS) {
      const preset = name as ViewportPreset;
      return { name: preset, size: VIEWPORT_PRESETS[preset] };
    }

    const match = /^(\d+)x(\d+)$/.exec(name);
    if (!match) {
      throw new Error(
        `unknown viewport "${name}"; use desktop, mobile, ipad-portrait, ipad-landscape, all, or WIDTHxHEIGHT`,
      );
    }

    const width = Number(match[1]);
    const height = Number(match[2]);
    if (width === 0 || height === 0) throw new Error("viewport dimensions must be greater than zero");
    return { name, size: { width, height } };
  });
}
