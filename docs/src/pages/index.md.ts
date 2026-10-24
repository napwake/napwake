// /index.md: the home as Markdown.
import type { APIRoute } from 'astro';
import { getEntry } from 'astro:content';
import { pageMarkdown } from './_markdown';

export const GET: APIRoute = async (context) => pageMarkdown((await getEntry('docs', 'index'))!, context);
