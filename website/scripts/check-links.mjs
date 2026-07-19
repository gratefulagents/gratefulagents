// Simple internal link checker for the built site.
import {readFileSync, readdirSync, statSync, existsSync} from 'node:fs';
import {join} from 'node:path';

const dist = new URL('../dist', import.meta.url).pathname;
const pages = [];
(function walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p);
    else if (name.endsWith('.html')) pages.push(p);
  }
})(dist);

let bad = 0;
for (const page of pages) {
  const html = readFileSync(page, 'utf8');
  for (const m of html.matchAll(/href="(\/[^"#]*)(#[^"]*)?"/g)) {
    const target = m[1];
    const file = target.endsWith('/') ? join(dist, target, 'index.html') : join(dist, target);
    if (!existsSync(file)) {
      console.log(`BROKEN ${target} in ${page.slice(dist.length)}`);
      bad++;
    }
  }
}
console.log(bad ? `${bad} broken links` : `OK: ${pages.length} pages, all internal links resolve`);
process.exit(bad ? 1 : 0);
