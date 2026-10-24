// /concepts/forecasting.md: every docs page but the home as Markdown (index.md.ts has the home).
import type { APIRoute, GetStaticPaths } from 'astro';
import { getCollection } from 'astro:content';
import { pageMarkdown } from './_markdown';

export const getStaticPaths = (async () => {
  const docs = await getCollection('docs', (d) => !d.data.draft && d.id !== 'index');
  return docs.map((entry) => ({ params: { slug: entry.id }, props: { entry } }));
}) satisfies GetStaticPaths;

export const GET: APIRoute = (context) => pageMarkdown(context.props.entry, context);
