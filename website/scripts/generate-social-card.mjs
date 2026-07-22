import sharp from 'sharp';

const width = 1200;
const height = 630;
const overlay = Buffer.from(`
<svg width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <linearGradient id="glow" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#7188d7" stop-opacity="0.22"/>
      <stop offset="1" stop-color="#0e171b" stop-opacity="0"/>
    </linearGradient>
  </defs>
  <rect width="1200" height="630" fill="#0e171b"/>
  <circle cx="1060" cy="-40" r="420" fill="url(#glow)"/>
  <path d="M0 520 H1200 M0 560 H1200 M0 600 H1200" stroke="#31434a" stroke-width="1" opacity="0.5"/>
  <path d="M870 0 V630 M930 0 V630 M990 0 V630 M1050 0 V630 M1110 0 V630 M1170 0 V630" stroke="#31434a" stroke-width="1" opacity="0.35"/>
  <rect x="80" y="82" width="108" height="108" rx="20" fill="#19282e" stroke="#93a2a4" stroke-opacity="0.5"/>
  <text x="80" y="275" fill="#efe8da" font-family="Arial, Helvetica, sans-serif" font-size="70" font-weight="700" letter-spacing="-2">Self-hosted coding agents</text>
  <text x="80" y="352" fill="#93a2a4" font-family="Arial, Helvetica, sans-serif" font-size="54" font-weight="700" letter-spacing="-1">for Kubernetes teams</text>
  <text x="80" y="445" fill="#d9a441" font-family="Arial, Helvetica, sans-serif" font-size="25" font-weight="700" letter-spacing="4">OPEN SOURCE · OBSERVABLE · YOUR COMPUTE</text>
  <text x="80" y="555" fill="#efe8da" font-family="Arial, Helvetica, sans-serif" font-size="31" font-weight="600">gratefulagents.dev</text>
</svg>`);

const logo = await sharp(new URL('../public/logo.png', import.meta.url).pathname)
  .resize(88, 88, {fit: 'contain'})
  .png()
  .toBuffer();

await sharp(overlay)
  .composite([{input: logo, left: 90, top: 92}])
  .png({compressionLevel: 9})
  .toFile(new URL('../public/og-default.png', import.meta.url).pathname);

console.log('Generated public/og-default.png (1200×630)');
