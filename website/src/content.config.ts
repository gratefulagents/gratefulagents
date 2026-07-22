import {defineCollection, z} from 'astro:content';
import {glob} from 'astro/loaders';

const docs = defineCollection({
  loader: glob({
    pattern: '**/*.md',
    base: '../user-docs/docs',
    // Always derive the id from the file path; ignore front-matter slugs
    // (intro.md declares `slug: /` for Docusaurus).
    generateId: ({entry}) => entry.replace(/\.md$/, ''),
  }),
  schema: z.object({
    title: z.string().optional(),
    seoTitle: z.string().max(70).optional(),
    description: z.string().max(160).optional(),
    slug: z.string().optional(),
    agentPrompt: z.string().optional(),
  }),
});

export const collections = {docs};
