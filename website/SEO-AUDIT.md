# SEO Audit: gratefulagents.dev

Audit date: 2026-07-22  
Scope: Astro marketing site and all 39 rendered documentation pages

## Executive summary

GratefulAgents has a strong technical-content foundation: static HTML, one descriptive H1 per page, substantial first-party documentation, useful internal navigation, descriptive screenshot alt text, explicit image dimensions, and transparent links to source and licensing.

The live site's primary SEO gaps were missing crawler discovery files, missing canonical and social metadata, boilerplate documentation descriptions, weak category language on the homepage, and no structured data. This change implements those fixes across the shared Astro layout and documentation renderer.

The remaining high-priority item is outside this repository's static-site implementation: `https://www.gratefulagents.dev/` presents an invalid TLS certificate. The hosting/DNS owner must provision that hostname and permanently redirect it to the apex domain.

## Site structure

- Canonical origin: `https://gratefulagents.dev`
- Pages generated: 40 (homepage plus 39 documentation pages)
- URL policy: lowercase, descriptive, slash-terminated routes
- Rendering: static Astro HTML; core content does not require JavaScript
- Documentation source: `user-docs/docs/`
- Documentation navigation: sidebar, contextual links, and previous/next links

### Findings

| Priority | Finding | Evidence | Resolution |
| --- | --- | --- | --- |
| High | No XML sitemap was available | Live `/sitemap-index.xml` returned 404 before this change | Added `@astrojs/sitemap`; the build now emits an index and a 40-URL sitemap shard |
| High | No robots file advertised crawl policy or sitemap location | Live `/robots.txt` returned 404 | Added `public/robots.txt` with an absolute sitemap URL |
| High | Canonical URLs were absent | Shared `Base.astro` emitted no canonical | Added query-free, absolute, self-referencing canonicals based on the configured production origin |
| Medium | One generated doc was absent from website navigation | `maintainer-work-item-cutover.md` existed in the source sidebar but not `website/src/data/sidebar.ts` | Restored it to website navigation, pager order, and internal linking |
| External | `www` hostname has invalid TLS | `https://www.gratefulagents.dev/` presented a GitHub-domain certificate during the audit | Provision valid TLS and add a one-hop permanent redirect to the apex domain |

## On-page SEO

### Homepage

Previous title and H1 described an "agent harness" but omitted the more discoverable category phrase "coding agents". "Self-hosted cloud-based" also made the deployment model less clear.

Implemented:

- Title: `Self-Hosted Coding Agents for Kubernetes | GratefulAgents`
- H1: `self-hosted coding agents for Kubernetes teams`
- A visible support paragraph that explains the runtime, tools, model providers, and observability proposition
- A concise, capability-based meta description

The copy deliberately avoids unverified claims about security certifications, enterprise readiness, performance, customer counts, or model quality.

### Documentation

Previously every page used the boilerplate description `gratefulagents user guide: [title]`.

Implemented:

- Optional `seoTitle` and `description` frontmatter fields
- Unique, cleaned first-paragraph descriptions as a fallback
- Search-oriented titles and hand-written descriptions for the highest-intent pages:
  - Local Kind deployment
  - k3s self-hosting
  - GitHub issue and PR automation
  - Skills, MCP servers, policies, and roles
  - Coding-agent observability
  - Pull-request review feedback
  - Run activity, traces, errors, and usage
- A build check that rejects missing, duplicate, or overlong descriptions and titles over 70 characters

## Metadata and structured data

Implemented centrally in `src/layouts/Base.astro`:

- `robots` directives with large image/snippet preview support
- Canonical URL
- Open Graph title, description, URL, type, locale, site name, and image metadata
- X/Twitter large-card metadata
- Theme color and Apple touch icon
- `Organization` and `WebSite` JSON-LD

Implemented by page type:

- Homepage: factual `SoftwareApplication` data covering the open-source license, repository, supported clients, integrations, runtime, and observable features
- Documentation: `TechArticle` and `BreadcrumbList` data, backed by visible breadcrumbs and a visible GratefulAgents maintainer statement

No ratings, reviews, prices, publication dates, or update dates were added because the repository does not contain reliable source data for those properties.

## Social preview

The site previously had no explicit social image, allowing unfurl systems to choose arbitrary page images.

Implemented:

- A branded 1200×630 PNG at `/og-default.png`
- Safe, high-contrast product-category copy
- Explicit image URL, MIME type, dimensions, and alt text
- A reproducible generator at `scripts/generate-social-card.mjs`

## Content and keyword opportunities

These are strategy opportunities, not measured ranking claims:

1. **Self-hosted coding agents on Kubernetes**
   - Target intents: self-hosted coding agent, open-source coding-agent platform, Kubernetes coding agent
   - Best next asset: an evaluator-focused category page linking to Kind, k3s, publishing, integrations, and observability
2. **GitHub issue-to-PR automation**
   - Target intents: GitHub issue coding agent, PR review agent, GitHub agent automation
   - The existing GitHub guide is the strongest starting page
3. **Agent operations and observability**
   - Target intents: coding-agent observability, AI agent tracing, agent cost monitoring
   - Link a future hub to Agent Ops, traces, run activity, and observability docs
4. **Agent governance and reusable resources**
   - Target intents: coding-agent guardrails, MCP server management, agent skills and policies
5. **Deployment decision support**
   - Best next asset: a substantive Kind-versus-k3s comparison, rather than thin keyword variants

## E-E-A-T and trust signals

Existing strengths:

- Public AGPL-3.0 source
- Detailed operational documentation
- Descriptive product screenshots
- "Edit this page" links
- Security policy and issue tracker in the repository

Implemented:

- Visible links to the security policy, releases, support issues, source, and docs source
- Visible documentation maintainer attribution
- Organization publisher/author relationships in structured data

Future improvement: derive truthful last-updated dates from Git history during the build. Do not hard-code dates that can become stale.

## Validation

`pnpm check` now validates:

- Astro production build
- All internal links
- Sitemap index and sitemap shard existence
- Robots sitemap declaration
- One canonical per page on the production origin
- Sitemap/canonical parity for all 40 pages
- Titles and unique descriptions within configured limits
- Open Graph URLs and resolvable social images
- Index/follow directives
- Parseable JSON-LD

## Post-deployment actions

1. Verify `/robots.txt`, `/sitemap-index.xml`, `/sitemap-0.xml`, and `/og-default.png` return 200.
2. Submit the sitemap index to Google Search Console and Bing Webmaster Tools.
3. Inspect the homepage, Kind, k3s, GitHub, Resources, and Observability URLs in Search Console.
4. Validate representative pages in Google Rich Results Test and Schema.org Validator.
5. Re-scrape social previews in LinkedIn, Facebook, and X tooling.
6. Provision valid TLS for `www` and permanently redirect every path/query to the equivalent apex URL.
7. Confirm the legacy `docs.gratefulagents.dev` Docusaurus hostname cannot expose a duplicate indexable copy; redirect it path-for-path if it is reactivated.
8. Establish Search Console and Core Web Vitals baselines before making performance claims or prioritizing speculative optimizations.

## Audit limitations

- No Search Console, analytics, backlink, or conversion data was provided.
- Core Web Vitals field data was not available.
- Structured data was verified in generated HTML and parsed during the build, but external rich-result eligibility must be rechecked after deployment.
- Keyword opportunities are based on product capabilities and search intent, not claimed current rankings.

## Sources checked

- `https://gratefulagents.dev/`
- `https://gratefulagents.dev/docs/getting-started/quick-start/`
- `https://gratefulagents.dev/docs/integrations/github/`
- `https://gratefulagents.dev/robots.txt`
- `https://gratefulagents.dev/sitemap-index.xml`
- `website/src/`, `website/public/`, `website/scripts/`, and `user-docs/docs/`
- Google Search documentation for sitemaps, snippets, titles, and canonicalization
- Astro's official sitemap integration documentation

## Rerun inputs

```yaml
workflow: firecrawl-seo-audit
site: https://gratefulagents.dev
keywords:
  - self-hosted coding agent
  - Kubernetes coding agent
  - GitHub issue coding agent
  - coding-agent observability
output: markdown
```
