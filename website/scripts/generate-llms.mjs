// Post-build script: writes dist/llms.txt following the llms.txt convention.
// Scans built HTML files for <title> and <meta name="description"> to derive
// entries automatically, so pages added by other agents are included.
import {readFileSync, writeFileSync, readdirSync, statSync} from 'node:fs';
import {join, relative, sep} from 'node:path';

const dist = new URL('../dist', import.meta.url).pathname;
const siteUrl = 'https://gratefulagents.dev';

// Collect all built index.html files.
const pages = [];
(function walk(dir) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) walk(full);
    else if (name.endsWith('.html')) pages.push(full);
  }
})(dist);

function extractMeta(html) {
  const rawTitle = html.match(/<title>([^<]+)<\/title>/)?.[1]?.trim() ?? '';
  // Strip " | GratefulAgents" suffix and variants for readability.
  const title = rawTitle.replace(/\s*\|\s*GratefulAgents.*$/, '').trim();
  const description = html.match(/<meta name="description" content="([^"]+)"/)?.[1]?.trim() ?? '';
  const isNoindex = html.includes('content="noindex');
  return {title, description, isNoindex};
}

const docEntries = [];
const pageEntries = [];

for (const file of pages) {
  const html = readFileSync(file, 'utf8');
  const builtPath = `/${relative(dist, file).split(sep).join('/')}`;
  const route = builtPath === '/index.html' ? '/' : builtPath.replace(/index\.html$/, '');
  const {title, description, isNoindex} = extractMeta(html);

  // Skip noindex pages (e.g. 404) from llms.txt.
  if (isNoindex) continue;
  if (!title || !description) continue;

  const url = `${siteUrl}${route}`;
  const line = `- [${title}](${url}): ${description}`;

  if (route.startsWith('/docs/')) {
    docEntries.push({route, line});
  } else {
    pageEntries.push({route, line});
  }
}

// Sort docs: /docs/ index first, then alphabetically by route.
docEntries.sort((a, b) => {
  if (a.route === '/docs/') return -1;
  if (b.route === '/docs/') return 1;
  return a.route.localeCompare(b.route);
});

// Sort pages: homepage first, then alphabetically.
pageEntries.sort((a, b) => {
  if (a.route === '/') return -1;
  if (b.route === '/') return 1;
  return a.route.localeCompare(b.route);
});

const lines = [
  '# GratefulAgents',
  '',
  '> Self-hosted control plane for running AI coding agents in your own Kubernetes cluster.',
  '',
  'GratefulAgents is an open-source (AGPL-3.0) self-hosted alternative to cloud coding agents such as Devin and the GitHub Copilot coding agent. It runs entirely in your own Kubernetes cluster — your credentials, your data, full audit trails of every agent turn.',
  '',
];

if (docEntries.length > 0) {
  lines.push('## Docs');
  lines.push('');
  for (const {line} of docEntries) lines.push(line);
  lines.push('');
}

if (pageEntries.length > 0) {
  lines.push('## Pages');
  lines.push('');
  for (const {line} of pageEntries) lines.push(line);
  lines.push('');
}

const output = lines.join('\n');
try {
  writeFileSync(join(dist, 'llms.txt'), output, 'utf8');
  console.log(`OK: wrote dist/llms.txt (${docEntries.length} docs, ${pageEntries.length} pages)`);
} catch (err) {
  console.error(`ERROR: failed to write dist/llms.txt: ${err.message}`);
  process.exit(1);
}
