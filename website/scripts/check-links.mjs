// Validate internal links and critical SEO signals in the built static site.
import {readFileSync, readdirSync, statSync, existsSync} from 'node:fs';
import {join, relative, sep} from 'node:path';

const dist = new URL('../dist', import.meta.url).pathname;
const origin = 'https://gratefulagents.dev';
const pages = [];
(function walk(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) walk(path);
    else if (name.endsWith('.html')) pages.push(path);
  }
})(dist);

let bad = 0;
const fail = (message) => {
  console.log(message);
  bad++;
};

const sitemapIndexPath = join(dist, 'sitemap-index.xml');
const sitemapPath = join(dist, 'sitemap-0.xml');
const robotsPath = join(dist, 'robots.txt');
const llmsPath = join(dist, 'llms.txt');
if (!existsSync(sitemapIndexPath)) fail('MISSING /sitemap-index.xml');
if (!existsSync(sitemapPath)) fail('MISSING /sitemap-0.xml');
if (!existsSync(robotsPath)) fail('MISSING /robots.txt');
if (!existsSync(llmsPath)) fail('MISSING /llms.txt');

const sitemapIndex = existsSync(sitemapIndexPath) ? readFileSync(sitemapIndexPath, 'utf8') : '';
const sitemap = existsSync(sitemapPath) ? readFileSync(sitemapPath, 'utf8') : '';
const robots = existsSync(robotsPath) ? readFileSync(robotsPath, 'utf8') : '';
if (!sitemapIndex.includes(`${origin}/sitemap-0.xml`)) fail('INVALID sitemap index location');
if (!robots.includes(`Sitemap: ${origin}/sitemap-index.xml`)) fail('INVALID robots.txt sitemap directive');
const sitemapUrls = new Set([...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((match) => match[1]));
const descriptions = new Map();
const titles = new Map();

// Doc ids listed in src/data/sidebar.ts (orderedIds); every built /docs/ route
// must appear there so no doc page is orphaned from the sidebar navigation.
const sidebarSource = readFileSync(new URL('../src/data/sidebar.ts', import.meta.url), 'utf8');
const sidebarIds = new Set();
for (const block of sidebarSource.matchAll(/items:\s*\[([^\]]*)\]/g)) {
  for (const id of block[1].matchAll(/'([^']+)'/g)) sidebarIds.add(id[1]);
}
const orphanDocs = [];

let indexablePageCount = 0;

for (const page of pages) {
  const html = readFileSync(page, 'utf8');
  const builtPath = `/${relative(dist, page).split(sep).join('/')}`;
  // index.html files map to their containing directory route; other .html
  // files (e.g. 404.html) map to /<stem>/ to match the rendered canonical.
  const route = builtPath === '/index.html' ? '/'
    : builtPath.endsWith('index.html') ? builtPath.replace(/index\.html$/, '')
    : builtPath.replace(/\.html$/, '/');
  const canonical = `${origin}${route}`;

  for (const match of html.matchAll(/href="(\/[^"#]*)(#[^"]*)?"/g)) {
    const target = match[1];
    const file = target.endsWith('/') ? join(dist, target, 'index.html') : join(dist, target);
    if (!existsSync(file)) fail(`BROKEN ${target} in ${route}`);
  }

  const title = html.match(/<title>([^<]+)<\/title>/)?.[1]?.trim();
  const description = html.match(/<meta name="description" content="([^"]+)"/)?.[1]?.trim();
  const renderedCanonical = html.match(/<link rel="canonical" href="([^"]+)"/)?.[1];
  const ogUrl = html.match(/<meta property="og:url" content="([^"]+)"/)?.[1];
  const ogImage = html.match(/<meta property="og:image" content="([^"]+)"/)?.[1];
  const robotsMeta = html.match(/<meta name="robots" content="([^"]+)"/)?.[1];
  const schemaText = html.match(/<script type="application\/ld\+json">([^<]+)<\/script>/)?.[1];

  // Noindex pages (e.g. 404) are exempt from indexability checks.
  const isNoindex = robotsMeta?.startsWith('noindex');

  if (!isNoindex && route.startsWith('/docs/')) {
    const docId = route === '/docs/' ? 'intro' : route.slice('/docs/'.length).replace(/\/$/, '');
    if (!sidebarIds.has(docId)) orphanDocs.push(`${route} (id: ${docId})`);
  }

  if (!title) fail(`MISSING title in ${route}`);
  else {
    if (title.length > 70) fail(`LONG title (${title.length}) in ${route}`);
    // Two indexable pages sharing a title compete for the same query.
    if (!isNoindex) {
      if (titles.has(title)) fail(`DUPLICATE title in ${route} and ${titles.get(title)}`);
      titles.set(title, route);
    }
  }
  if (!description) fail(`MISSING description in ${route}`);
  else {
    if (description.length < 70) console.warn(`WARN: short description (${description.length} chars) in ${route}`);
    if (description.length > 160) fail(`LONG description (${description.length}) in ${route}`);
    if (descriptions.has(description)) fail(`DUPLICATE description in ${route} and ${descriptions.get(description)}`);
    descriptions.set(description, route);
  }
  if (renderedCanonical !== canonical) fail(`INVALID canonical ${renderedCanonical ?? '(missing)'} in ${route}`);
  if (ogUrl !== canonical) fail(`INVALID og:url ${ogUrl ?? '(missing)'} in ${route}`);
  if (!ogImage?.startsWith(`${origin}/`)) fail(`INVALID og:image in ${route}`);
  else {
    const imagePath = new URL(ogImage).pathname;
    if (!existsSync(join(dist, imagePath))) fail(`MISSING social image ${imagePath} in ${route}`);
  }
  if (!isNoindex && !robotsMeta?.startsWith('index, follow')) fail(`INVALID robots meta in ${route}`);
  if (!schemaText) fail(`MISSING JSON-LD in ${route}`);
  else {
    try {
      JSON.parse(schemaText);
    } catch {
      fail(`INVALID JSON-LD in ${route}`);
    }
  }
  if (!isNoindex) {
    if (!sitemapUrls.has(canonical)) fail(`MISSING sitemap URL ${canonical}`);
    indexablePageCount++;
  }

  // Every page must have exactly one <h1>.
  const h1Matches = [...html.matchAll(/<h1[\s>]/g)];
  if (h1Matches.length === 0) fail(`MISSING h1 in ${route}`);
  else if (h1Matches.length > 1) fail(`MULTIPLE h1 (${h1Matches.length}) in ${route}`);

  // Every <img> must have an alt attribute (empty string is valid for decorative images).
  for (const match of html.matchAll(/<img\s[^>]*>/g)) {
    if (!/\balt=/i.test(match[0])) fail(`IMG missing alt in ${route}: ${match[0].slice(0, 80)}`);
  }
}

if (sitemapUrls.size !== indexablePageCount) {
  fail(`SITEMAP count ${sitemapUrls.size} does not match indexable page count ${indexablePageCount}`);
}

if (orphanDocs.length > 0) {
  fail(
    `ORPHAN docs route(s) not listed in src/data/sidebar.ts orderedIds — add them to a sidebar section or move the source out of user-docs/docs:\n  ${orphanDocs.join('\n  ')}`,
  );
}

console.log(bad
  ? `${bad} validation error(s)`
  : `OK: ${pages.length} pages, internal links and SEO signals validated`);
process.exit(bad ? 1 : 0);
