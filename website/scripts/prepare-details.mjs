// Derives zoomed, tone-lifted detail crops from the full-app screenshots.
//
// The product UI is a dark theme (mean luminance around 15/255) captured at
// 3024px wide. Dropped whole into a 700px card on a near-black page it reads as
// an empty rectangle: the interface is there, but nothing is legible and it has
// no separation from the background. Each entry below therefore takes the
// information-dense region of a shot, lifts the black point so the panel reads
// as a surface, and resizes once so the browser never downscales by 4x.
//
// Regions are fractions of the source so they survive a re-capture at another
// resolution. Run with `pnpm details` after replacing anything in
// public/screens/.
import sharp from 'sharp';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const dir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../public/screens');

/**
 * left/top are fractions of the source. width is a fraction of the source
 * width; height is derived from `ratio` so every output has a known aspect.
 * @type {{from: string, to: string, left: number, top: number, width: number, ratio: number, out: number}[]}
 */
const crops = [
  // Hero-adjacent detail: the conversation column of a live run.
  {from: 'run-chat.webp', to: 'run-chat-detail.webp', left: 0.02, top: 0.08, width: 0.61, ratio: 16 / 9, out: 1400},
  // Tablet: the run as followed from a second device.
  {from: 'tablet-1.webp', to: 'tablet-1-detail.webp', left: 0.09, top: 0.14, width: 0.76, ratio: 3 / 2, out: 1200},
  // Gallery views: main workspace only, sidebar and empty canvas trimmed.
  // Gallery views: the workspace at full width, navigation chrome and the dead
  // canvas below the content trimmed, all four normalised to 16:10 so switching
  // tabs never shifts the layout.
  {from: 'agent-ops.webp', to: 'agent-ops-detail.webp', left: 0.17, top: 0.0, width: 0.82, ratio: 16 / 10, out: 2200},
  {from: 'observability.webp', to: 'observability-detail.webp', left: 0.17, top: 0.03, width: 0.82, ratio: 16 / 10, out: 2200},
  {from: 'graph.webp', to: 'graph-detail.webp', left: 0.17, top: 0.03, width: 0.82, ratio: 16 / 10, out: 2200},
  {from: 'trace.webp', to: 'trace-detail.webp', left: 0.17, top: 0.05, width: 0.82, ratio: 16 / 10, out: 2200},
];

for (const crop of crops) {
  const source = path.join(dir, crop.from);
  const {width = 0, height = 0} = await sharp(source).metadata();
  const cropWidth = Math.round(width * crop.width);
  const cropHeight = Math.min(Math.round(cropWidth / crop.ratio), height - Math.round(height * crop.top));

  const output = await sharp(source)
    .extract({
      left: Math.round(width * crop.left),
      top: Math.round(height * crop.top),
      width: cropWidth,
      height: cropHeight,
    })
    // Lift the black point instead of scaling brightness: a multiplier leaves a
    // near-black screenshot near-black, an offset gives the panel a floor.
    .linear(1.08, 16)
    .resize({width: crop.out})
    .webp({quality: 88})
    .toFile(path.join(dir, crop.to));

  console.log(`${crop.from} -> ${crop.to} (${output.width}x${output.height})`);
}
