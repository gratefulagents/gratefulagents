// @ts-check
import {defineConfig} from 'astro/config';
import sitemap from '@astrojs/sitemap';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {existsSync} from 'node:fs';
import {execFileSync} from 'node:child_process';

const docsRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../user-docs/docs');

/**
 * Rewrites relative intra-docs markdown links (./foo.md, ../runs/bar.md) to
 * their rendered routes under /docs/, so the markdown stays single-sourced
 * in user-docs/ and keeps working on GitHub.
 */
function rewriteDocLinks() {
  /** @param {any} node @param {(n: any) => void} fn */
  const walk = (node, fn) => {
    fn(node);
    if (node.children) for (const child of node.children) walk(child, fn);
  };
  return (/** @type {any} */ tree, /** @type {any} */ file) => {
    const rel = path.relative(docsRoot, file.path);
    if (rel.startsWith('..')) return;
    const dir = path.posix.dirname(rel.split(path.sep).join('/'));
    walk(tree, (node) => {
      if (node.type !== 'link' || typeof node.url !== 'string') return;
      const m = node.url.match(/^([^:#?]+)\.md(#.*)?$/);
      if (!m) return;
      const target = path.posix.normalize(path.posix.join(dir === '.' ? '' : dir, m[1]));
      node.url = (target === 'intro' ? '/docs/' : `/docs/${target}/`) + (m[2] ?? '');
    });
  };
}

/**
 * Resolves the source file that produces a given route, so sitemap `lastmod`
 * can report when the page actually changed instead of when it was last built.
 */
function sourceFileForRoute(route) {
  const here = path.dirname(fileURLToPath(import.meta.url));
  if (route === '/') return path.join(here, 'src/pages/index.astro');
  if (route === '/docs/') return path.join(docsRoot, 'intro.md');
  if (route.startsWith('/docs/')) {
    return path.join(docsRoot, `${route.slice('/docs/'.length).replace(/\/$/, '')}.md`);
  }
  const slug = route.replace(/^\/|\/$/g, '');
  // A route like /alternatives/devin/ may come from either alternatives/devin.astro
  // or alternatives/devin/index.astro.
  for (const candidate of [`src/pages/${slug}.astro`, `src/pages/${slug}/index.astro`]) {
    const file = path.join(here, candidate);
    if (existsSync(file)) return file;
  }
  return undefined;
}

const lastmodCache = new Map();

/** Last git commit date (YYYY-MM-DD) for a route's source file, if available. */
function lastmodForRoute(route) {
  if (lastmodCache.has(route)) return lastmodCache.get(route);
  let result;
  const file = sourceFileForRoute(route);
  if (file && existsSync(file)) {
    try {
      const out = execFileSync('git', ['log', '-1', '--format=%cI', '--', file], {
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'ignore'],
      }).trim();
      // Empty for files that are not committed yet; omit lastmod rather than guess.
      if (out) result = out.slice(0, 10);
    } catch {
      // No git available (e.g. a tarball build) — omit lastmod.
    }
  }
  lastmodCache.set(route, result);
  return result;
}

export default defineConfig({
  site: 'https://gratefulagents.dev',
  trailingSlash: 'always',
  integrations: [
    sitemap({
      filter: (page) => !page.endsWith('/404/'),
      serialize: (item) => {
        const path = item.url.replace('https://gratefulagents.dev', '');
        const lastmod = lastmodForRoute(path);
        const withLastmod = (rest) => (lastmod ? {...item, ...rest, lastmod} : {...item, ...rest});

        if (path === '/') {
          return withLastmod({priority: 1.0, changefreq: 'weekly'});
        }
        if (
          path === '/docs/' ||
          path === '/docs/getting-started/self-hosting-kind/' ||
          path === '/docs/getting-started/self-hosting-k3s/' ||
          path === '/docs/getting-started/quick-start/'
        ) {
          return withLastmod({priority: 0.9, changefreq: 'weekly'});
        }
        if (!path.startsWith('/docs/')) {
          return withLastmod({priority: 0.9, changefreq: 'monthly'});
        }
        return withLastmod({priority: 0.7, changefreq: 'monthly'});
      },
    }),
  ],
  markdown: {
    remarkPlugins: [rewriteDocLinks],
  },
});
