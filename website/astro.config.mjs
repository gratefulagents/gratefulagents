// @ts-check
import {defineConfig} from 'astro/config';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

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

export default defineConfig({
  site: 'https://gratefulagents.dev',
  trailingSlash: 'always',
  markdown: {
    remarkPlugins: [rewriteDocLinks],
  },
});
