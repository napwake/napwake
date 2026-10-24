// Each docs page as Markdown, for readers and assistants: the same conversion as llms-full.txt
// (starlight-llms-txt's), headed by the page's title, description and address.
import type { APIContext } from 'astro';
import type { CollectionEntry } from 'astro:content';
// The plugin exports only its integration; its converter is imported by path, at the pinned version.
import { entryToSimpleMarkdown } from '../../node_modules/starlight-llms-txt/entryToSimpleMarkdown.ts';

export async function pageMarkdown(entry: CollectionEntry<'docs'>, context: APIContext) {
  const id = entry.id === 'index' ? '' : entry.id;
  const url = new URL(import.meta.env.BASE_URL.replace(/\/?$/, '/') + (id ? `${id}/` : ''), context.site).href;
  // links absolute, so a copy pasted into an assistant still points somewhere
  const origin = new URL(context.site!).origin;
  const body = (await entryToSimpleMarkdown(entry, context, false))
    .replace(/\]\((\/[^)\s]*)\)/g, (all, path) => `](${origin}${path})`)
    .replace(/\]\((#[^)\s]*)\)/g, (all, hash) => `](${url}${hash})`);
  const lines = [`# ${entry.data.title}`];
  const summary = entry.data.lede ?? entry.data.description;
  if (summary) lines.push(`> ${summary}`);
  lines.push(`Source: ${url}`, body);
  return new Response(lines.join('\n\n') + '\n', { headers: { 'Content-Type': 'text/markdown; charset=utf-8' } });
}
