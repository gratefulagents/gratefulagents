// Prepares the hero run recording for the website:
//  - crops away the same 66px OS band that prepare-shots.mjs trims
//  - downscales to 2400px wide, 30fps, and writes a faststart MP4
//  - extracts a poster frame (the subagent-DAG moment) as webp
//
// Input: website/raw-shots/run-recording-4x.mov — a 4x-speed screen
// recording of one full run (not tracked in git; drop a fresh capture
// there to regenerate). Requires ffmpeg on PATH.
// Run: node scripts/prepare-video.mjs
import {execFileSync} from 'node:child_process';
import {existsSync, mkdirSync, rmSync} from 'node:fs';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import sharp from 'sharp';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const src = join(root, 'raw-shots', 'run-recording-4x.mov');
const outDir = join(root, 'public', 'screens');
mkdirSync(outDir, {recursive: true});

if (!existsSync(src)) {
  console.error('missing raw-shots/run-recording-4x.mov');
  process.exit(1);
}

// Same top crop as the desktop screenshots: 3024x1964 raw → 3024x1898.
const FILTER = 'crop=3024:1898:0:66,scale=2400:-2,fps=30';
const mp4 = join(outDir, 'run-recording.mp4');

execFileSync('ffmpeg', [
  '-y', '-v', 'error',
  '-i', src,
  '-vf', FILTER,
  '-an',
  '-c:v', 'libx264', '-crf', '23', '-preset', 'slow',
  '-pix_fmt', 'yuv420p',
  '-movflags', '+faststart',
  mp4,
]);
console.log('wrote public/screens/run-recording.mp4');

// Poster: ~20s in, when the run is fanning out to the subagent DAG.
const framePng = join(outDir, 'run-recording-poster.tmp.png');
execFileSync('ffmpeg', ['-y', '-v', 'error', '-ss', '20', '-i', mp4, '-frames:v', '1', framePng]);
await sharp(framePng).webp({quality: 84}).toFile(join(outDir, 'run-recording-poster.webp'));
rmSync(framePng);
console.log('wrote public/screens/run-recording-poster.webp');
