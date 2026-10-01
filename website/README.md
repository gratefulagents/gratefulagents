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
- `src/pages/docs/[...slug].astro` — docs shell: sidebar, prose, prev/next
- `src/data/sidebar.ts` — navigation order (mirrors `user-docs/sidebars.ts`)
- `src/content.config.ts` — content collection over `../user-docs/docs`
- `astro.config.mjs` — remark plugin that rewrites `.md` links to routes
- `scripts/check-links.mjs` — internal link checker for the built site

## Design language

Paper and ink. The pages are quiet so the dark product screenshots carry the
weight, and the copy does the selling. The site should read like an
infrastructure project's site, not a generated SaaS template.

- Positioning: infrastructure for running many coding agents in the cloud.
  Lead with parallel, isolated, persistent runs on a cluster you control, and
  contrast with an agent CLI stuck on one laptop. Self-hosting is the means,
  not the headline.
- Palette: paper `#F3F0E8`, ink `#17160F`, rules `#D3CDBD`, one vermilion
  accent `#C2361C` used for emphasis only, near-black `#16161A` for code and
  screenshot bands.
- Type: Newsreader (display), IBM Plex Sans (body and UI), IBM Plex Mono (code
  and resource names).
- Structure comes from hairline rules and tables, not cards, gradients, glows,
  or icon grids. No eyebrow labels on every section, no emoji.
- Copy: plain sentences, concrete nouns, no em-dash flourishes, and no claims
  the docs don't back up. If a feature is partial, say so.
- Page flow: headline + install command → recorded run → laptop vs cluster
  table → how runs scale (RuntimeProfile) → triggers and review loop →
  product views → what stays on your cluster → FAQ → docs index → closing CTA.
- Docusaurus `:::note` / `:::warning` blocks in `user-docs` are rendered as
  callouts by the `admonitions` remark plugin in `astro.config.mjs`.
- Brand marks in `src/data/marks.ts`, rendered monochrome via
  `src/components/Mark.astro`; screenshots processed by
  `scripts/prepare-shots.mjs` (`pnpm shots`).
