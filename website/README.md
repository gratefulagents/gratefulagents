# gratefulagents website

The public website for gratefulagents: a designed landing page plus the full
user guide, built with [Astro](https://astro.build) and **pnpm**.

The documentation is **single-sourced**: pages under `../user-docs/docs/*.md`
are loaded directly via an Astro content collection and rendered into
`/docs/...` routes. Edit docs in `user-docs/`; this site picks them up on the
next build. Relative markdown links (`./page.md`) are rewritten to site routes
at build time, so the markdown keeps working on GitHub too.

## Commands

```sh
pnpm install
pnpm dev       # local dev server
pnpm build     # static build → dist/
pnpm check     # build + verify all internal links resolve
```

## Structure

- `src/pages/index.astro` — landing page
- `src/pages/faq.astro`, `src/pages/use-cases/` — marketing and SEO pages
- `src/pages/docs/[...slug].astro` — docs shell: sidebar, prose, prev/next
- `src/data/sidebar.ts` — navigation order (mirrors `user-docs/sidebars.ts`)
- `src/content.config.ts` — content collection over `../user-docs/docs`
- `astro.config.mjs` — remark plugin that rewrites `.md` links to routes
- `scripts/check-links.mjs` — internal link checker for the built site

## Design language

"Instrument panel". Dark-locked, cool near-black neutrals with a single
saturated accent that is only ever used for action or live state, so colour
still means something on a page full of dark product screenshots.

- Palette: page `#08090B`, raised `#0E1013`, panel `#14171C`, ink `#EEF0F3`,
  muted `#C2C8D1`, quiet `#959CA7`, accent signal green `#3DDC97`
- Type: Geist (display and body) and Geist Mono (identifiers, metadata,
  terminal), self-hosted via `@fontsource-variable` so first paint never waits
  on a third-party stylesheet
- Shape: one documented system. Interactive controls use `--r-ctl` (10px),
  surfaces and media use `--r-surface` (16px). Nothing else adds a radius.
- Page flow, each section a different layout family: split hero with the run
  recording, trigger/model rail, terminal install panel, run pipeline, tabbed
  product gallery, asymmetric bento, use-case index, FAQ disclosure list,
  closing statement
- Motion is limited and motivated: the hero recording plays only while it is on
  screen, and section headings reveal once on entry. Both collapse to static
  under `prefers-reduced-motion`, and `.reveal` is only armed once the inline
  `js` class is set, so content is never hidden behind script that failed.
- Product screenshots are a near-black dark theme (mean luminance around
  15/255). `scripts/prepare-details.mjs` (`pnpm details`) derives the cropped,
  black-point-lifted `*-detail.webp` variants the page actually uses; dropping a
  full 3024px app capture into a card renders an unreadable black rectangle.
- Styles are split by surface: `styles/global.css` (tokens, shell, utilities)
  imports `home.css`, `page.css`, and `docs.css`
- Brand marks in `src/data/marks.ts`, rendered monochrome via
  `src/components/Mark.astro`; raw screenshots processed by
  `scripts/prepare-shots.mjs` (`pnpm shots`)

## Content and SEO surfaces

- `/` landing page, with `SoftwareApplication` and `FAQPage` structured data
- `/faq/` full FAQ, `FAQPage` + `BreadcrumbList`, sourced from `src/data/faq.ts`
- `/use-cases/` index and four detail pages, `Article` + `HowTo` +
  `BreadcrumbList`, sourced from `src/data/usecases.ts`
- `/docs/...` the single-sourced user guide, `TechArticle` + `BreadcrumbList`

Copy in `src/data/*.ts` is plain text on purpose: the same strings feed visible
page copy and JSON-LD, so they must not contain markup.
