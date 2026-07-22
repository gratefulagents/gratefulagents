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
if (!existsSync(sitemapIndexPath)) fail('MISSING /sitemap-index.xml');
if (!existsSync(sitemapPath)) fail('MISSING /sitemap-0.xml');
if (!existsSync(robotsPath)) fail('MISSING /robots.txt');

const sitemapIndex = existsSync(sitemapIndexPath) ? readFileSync(sitemapIndexPath, 'utf8') : '';
const sitemap = existsSync(sitemapPath) ? readFileSync(sitemapPath, 'utf8') : '';
const robots = existsSync(robotsPath) ? readFileSync(robotsPath, 'utf8') : '';
if (!sitemapIndex.includes(`${origin}/sitemap-0.xml`)) fail('INVALID sitemap index location');
if (!robots.includes(`Sitemap: ${origin}/sitemap-index.xml`)) fail('INVALID robots.txt sitemap directive');
const sitemapUrls = new Set([...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((match) => match[1]));
const descriptions = new Map();

for (const page of pages) {
  const html = readFileSync(page, 'utf8');
  const builtPath = `/${relative(dist, page).split(sep).join('/')}`;
  const route = builtPath === '/index.html' ? '/' : builtPath.replace(/index\.html$/, '');
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

  if (!title) fail(`MISSING title in ${route}`);
  else if (title.length > 70) fail(`LONG title (${title.length}) in ${route}`);
  if (!description) fail(`MISSING description in ${route}`);
  else {
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
  if (!robotsMeta?.startsWith('index, follow')) fail(`INVALID robots meta in ${route}`);
  if (!schemaText) fail(`MISSING JSON-LD in ${route}`);
  else {
    try {
      JSON.parse(schemaText);
    } catch {
      fail(`INVALID JSON-LD in ${route}`);
    }
  }
  if (!sitemapUrls.has(canonical)) fail(`MISSING sitemap URL ${canonical}`);
}

if (sitemapUrls.size !== pages.length) {
  fail(`SITEMAP count ${sitemapUrls.size} does not match page count ${pages.length}`);
}

console.log(bad
  ? `${bad} validation error(s)`
  : `OK: ${pages.length} pages, internal links and SEO signals validated`);
process.exit(bad ? 1 : 0);
