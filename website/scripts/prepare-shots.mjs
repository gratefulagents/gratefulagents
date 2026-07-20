// Prepares product screenshots for the website:
//  - crops away OS chrome (iPad status bar), keeping the app window
//  - resizes and writes optimized webp to public/screens/
//
// Input: raw retina screenshots in website/raw-shots/ (not tracked in
// git — drop fresh captures there to regenerate).
// Run: node scripts/prepare-shots.mjs
import sharp from 'sharp';
import {existsSync, mkdirSync} from 'node:fs';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const outDir = join(root, 'public', 'screens');
mkdirSync(outDir, {recursive: true});

// name → source screenshot in raw-shots/. `crop: null` publishes the raw
// capture as-is (for clean window captures without the baked-in top band).
const shots = [
  {out: 'agent-ops', src: 'agent-ops.png'},
  {out: 'run-chat', src: 'run-chat.png'},
  {out: 'trace', src: 'trace.png'},
  {out: 'observability', src: 'observability.png', crop: null},
  {out: 'graph', src: 'graph.png'},
];

// iPad screenshots (iPad Pro 13" landscape, 2752x2064 @2x): published whole —
// the iPadOS status bar (clock, date, battery) stays in to read as a device.
const TABLET = [
  {out: 'tablet-1', src: 'ipad-1.png'},
  {out: 'tablet-2', src: 'ipad-2.png'},
];

for (const {out, src} of TABLET) {
  const srcPath = join(root, 'raw-shots', src);
  if (!existsSync(srcPath)) {
    console.error(`missing ${src}`);
    process.exitCode = 1;
    continue;
  }
  await sharp(srcPath)
    .webp({quality: 84})
    .toFile(join(outDir, `${out}.webp`));
  console.log(`wrote public/screens/${out}.webp`);
}

// Desktop raws are 3024x1964 full-app captures with a black band baked in at
// the top (~66px). Trim it, publish at native resolution — the site shows
// these near full-width on retina screens, so do not downscale.
const DESKTOP_CROP = {left: 0, top: 66, width: 3024, height: 1964 - 66};

for (const shot of shots) {
  const {out, src} = shot;
  const srcPath = join(root, 'raw-shots', src);
  if (!existsSync(srcPath)) {
    console.error(`missing ${src}`);
    process.exitCode = 1;
    continue;
  }
  const crop = 'crop' in shot ? shot.crop : DESKTOP_CROP;
  let img = sharp(srcPath);
  if (crop) img = img.extract(crop);
  await img
    .webp({quality: 84})
    .toFile(join(outDir, `${out}.webp`));
  console.log(`wrote public/screens/${out}.webp`);
}
