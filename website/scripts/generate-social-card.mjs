import sharp from 'sharp';

const width = 1200;
const height = 630;
const overlay = Buffer.from(`
<svg width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <linearGradient id="glow" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#3ddc97" stop-opacity="0.16"/>
      <stop offset="1" stop-color="#08090b" stop-opacity="0"/>
    </linearGradient>
  </defs>
  <rect width="1200" height="630" fill="#08090b"/>
  <circle cx="1080" cy="-60" r="440" fill="url(#glow)"/>
  <rect x="0" y="0" width="1200" height="3" fill="#3ddc97" opacity="0.65"/>
  <rect x="80" y="82" width="108" height="108" rx="20" fill="#14171c" stroke="#3ddc97" stroke-opacity="0.35"/>
  <circle cx="84" cy="272" r="7" fill="#3ddc97"/>
  <text x="104" y="279" fill="#959ca7" font-family="Menlo, monospace" font-size="22" font-weight="500" letter-spacing="3">OPEN SOURCE · KUBERNETES NATIVE</text>
  <text x="80" y="372" fill="#eef0f3" font-family="Arial, Helvetica, sans-serif" font-size="72" font-weight="700" letter-spacing="-2.5">Coding agents that run</text>
  <text x="80" y="452" fill="#eef0f3" font-family="Arial, Helvetica, sans-serif" font-size="72" font-weight="700" letter-spacing="-2.5">inside your cluster</text>
  <text x="80" y="548" fill="#c2c8d1" font-family="Arial, Helvetica, sans-serif" font-size="30" font-weight="600">gratefulagents.dev</text>
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
