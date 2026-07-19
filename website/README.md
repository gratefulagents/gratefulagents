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

- `src/pages/index.astro` — landing page ("run schematic" hero)
- `src/pages/docs/[...slug].astro` — docs shell: sidebar, prose, prev/next
- `src/data/sidebar.ts` — navigation order (mirrors `user-docs/sidebars.ts`)
- `src/content.config.ts` — content collection over `../user-docs/docs`
- `astro.config.mjs` — remark plugin that rewrites `.md` links to routes
- `scripts/check-links.mjs` — internal link checker for the built site

## Design language

“Quiet industrial control room” — a dark-first, screenshot-led system that
presents gratefulagents as operational infrastructure rather than another chat
assistant. The first viewport contains the product promise, a live desktop and
mobile run, and the full trigger/model capability dock.

- Palette: deep petrol `#0E171B`, raised slate `#19282E`, warm bone `#EFE8DA`,
  muted steel `#93A2A4`, product indigo `#7188D7`, and routing brass `#D9A441`
- Type: Schibsted Grotesk (display), Geist (body/UI), and Geist Mono
  (run identifiers, states, and operational metadata)
- Hero proof stays direct: unframed desktop and mobile product screenshots,
  followed immediately by the trigger and model capability dock
- Page flow: single-viewport product proof + capability dock → product gallery
  → observability and infrastructure proof → deployment path → docs map →
  final self-hosting CTA
- Product gallery views change only on user input; there is no autoplay
- Brand marks in `src/data/marks.ts`, rendered monochrome via
  `src/components/Mark.astro`; screenshots processed by
  `scripts/prepare-shots.mjs` (`pnpm shots`)
