// A small markdown renderer for the docs (build time only). It covers what the docs use:
// ATX headings with anchors, paragraphs, emphasis, code spans, links, images, fenced code with
// a copy button and light highlighting, block quotes with [!NOTE]/[!TIP]/[!WARNING] callouts,
// nested lists, tables and rules. Raw HTML in the source is escaped, not passed through.
import { highlight, language } from './highlight';
import { treeHTML, treeText } from './tree';

export type Heading = { depth: number; text: string; id: string };

export type Rendered = { html: string; headings: Heading[] };

export type Options = {
	/** Rewrites a link target (e.g. ../guide/02-x.md to /docs/guide/x). */
	link?: (href: string) => string;
	/** Drops a leading "# title" that repeats the page title. */
	title?: string;
};

const esc = (s: string) =>
	s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

/** Lower-case, dash-separated anchor id of a heading text. */
export function slugify(text: string): string {
	return (
		text
			.toLowerCase()
			.replace(/<[^>]*>/g, '')
			.replace(/[`*_~[\]()]/g, '')
			.replace(/[^\p{L}\p{N}\s-]/gu, '')
			.trim()
			.replace(/\s+/g, '-') || 'section'
	);
}

/** Plain text of inline markdown (for headings in the toc and the search index). */
export function plain(md: string): string {
	return md
		.replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
		.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
		.replace(/[`*_~]/g, '')
		.trim();
}

function inline(src: string, opt: Options): string {
	// Code spans first: their content is literal.
	const codes: string[] = [];
	let s = src.replace(/(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)/g, (_, _t, c: string) => {
		codes.push(`<code>${esc(c.trim() === '' ? c : c.replace(/^ (.*) $/, '$1'))}</code>`);
		return `\u0000${codes.length - 1}\u0000`;
	});
	s = esc(s);
	const href = (h: string) => esc(opt.link ? opt.link(h.replace(/&amp;/g, '&')) : h.replace(/&amp;/g, '&'));
	s = s
		.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+&quot;[^&]*&quot;)?\)/g, (_, alt, h) => `<img src="${href(h)}" alt="${alt}" loading="lazy">`)
		.replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+&quot;[^&]*&quot;)?\)/g, (_, text, h: string) => {
			const ext = /^https?:\/\//.test(h);
			return `<a href="${href(h)}"${ext ? ' rel="external noopener"' : ''}>${text}</a>`;
		})
		.replace(/&lt;(https?:\/\/[^\s&]+)&gt;/g, '<a href="$1" rel="external noopener">$1</a>')
		.replace(/\*\*(?=\S)([\s\S]*?\S)\*\*/g, '<strong>$1</strong>')
		.replace(/__(?=\S)([\s\S]*?\S)__/g, '<strong>$1</strong>')
		.replace(/(^|[^\w*])\*(?=\S)([^*]*?\S)\*(?!\w)/g, '$1<em>$2</em>')
		.replace(/(^|[^\w])_(?=\S)([^_]*?\S)_(?!\w)/g, '$1<em>$2</em>')
		.replace(/~~(?=\S)([\s\S]*?\S)~~/g, '<del>$1</del>')
		.replace(/ {2,}\n|\\\n/g, '<br>\n');
	return s.replace(/\u0000(\d+)\u0000/g, (_, i) => codes[Number(i)]);
}

const reFence = /^(\s*)(`{3,}|~{3,})(.*)$/;
const reHeading = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const reHr = /^\s{0,3}([-*_])(\s*\1){2,}\s*$/;
const reItem = /^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const reTableSep = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/;
const callouts: Record<string, string> = {
	NOTE: 'Note',
	TIP: 'Tip',
	IMPORTANT: 'Important',
	WARNING: 'Warning',
	CAUTION: 'Caution'
};

function cells(row: string): string[] {
	let r = row.trim();
	if (r.startsWith('|')) r = r.slice(1);
	if (r.endsWith('|') && !r.endsWith('\\|')) r = r.slice(0, -1);
	return r.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, '|'));
}

class Renderer {
	headings: Heading[] = [];
	ids = new Map<string, number>();
	constructor(private opt: Options) {}

	id(text: string): string {
		const base = slugify(text);
		const n = this.ids.get(base) ?? 0;
		this.ids.set(base, n + 1);
		return n === 0 ? base : `${base}-${n}`;
	}

	blocks(lines: string[], top: boolean): string {
		const out: string[] = [];
		let i = 0;
		const isBlank = (l: string | undefined) => l === undefined || l.trim() === '';
		const startsBlock = (l: string) =>
			reFence.test(l) || reHeading.test(l) || reHr.test(l) || /^\s*>/.test(l) || reItem.test(l);
		while (i < lines.length) {
			const line = lines[i];
			if (isBlank(line)) {
				i++;
				continue;
			}
			let m: RegExpMatchArray | null;
			if ((m = line.match(reFence))) {
				const [, indent, fence, info] = m;
				const body: string[] = [];
				i++;
				while (i < lines.length && !(lines[i].trim().startsWith(fence[0].repeat(fence.length)) && lines[i].trim().replace(/[`~]/g, '') === '')) {
					body.push(lines[i].startsWith(indent) ? lines[i].slice(indent.length) : lines[i].trimStart());
					i++;
				}
				i++;
				out.push(this.code(body.join('\n'), info));
				continue;
			}
			if ((m = line.match(reHeading))) {
				i++;
				let depth = m[1].length;
				const text = m[2];
				if (depth === 1 && top && this.opt.title !== undefined && out.length === 0 && plain(text) === this.opt.title) continue;
				if (depth === 1) depth = 2; // the page title is the only h1
				const id = this.id(plain(text));
				if (depth <= 3) this.headings.push({ depth, text: plain(text), id });
				out.push(
					`<h${depth} id="${id}">${inline(text, this.opt)}<a class="anchor" href="#${id}" aria-label="Link to this section">#</a></h${depth}>`
				);
				continue;
			}
			if (reHr.test(line)) {
				i++;
				out.push('<hr>');
				continue;
			}
			if (/^\s*>/.test(line)) {
				const body: string[] = [];
				while (i < lines.length && !isBlank(lines[i]) && (/^\s*>/.test(lines[i]) || body.length > 0)) {
					if (!/^\s*>/.test(lines[i]) && startsBlock(lines[i])) break;
					body.push(lines[i].replace(/^\s*> ?/, ''));
					i++;
				}
				out.push(this.quote(body));
				continue;
			}
			if (reItem.test(line)) {
				const start = i;
				const indent = line.match(reItem)![1].length;
				i++;
				while (i < lines.length) {
					const l = lines[i];
					if (isBlank(l)) {
						// a blank line continues the list when the next line is indented or another item
						const next = lines[i + 1];
						if (next !== undefined && !isBlank(next) && (next.match(/^\s*/)![0].length > indent || (reItem.test(next) && next.match(reItem)![1].length >= indent))) {
							i++;
							continue;
						}
						break;
					}
					const im = l.match(reItem);
					if (im && im[1].length < indent) break;
					if (!im && l.match(/^\s*/)![0].length <= indent && startsBlock(l)) break;
					i++;
				}
				out.push(this.list(lines.slice(start, i), indent));
				continue;
			}
			if (line.includes('|') && i + 1 < lines.length && reTableSep.test(lines[i + 1])) {
				const head = cells(line);
				const align = cells(lines[i + 1]).map((c) =>
					c.startsWith(':') && c.endsWith(':') ? 'center' : c.endsWith(':') ? 'right' : ''
				);
				i += 2;
				const rows: string[][] = [];
				while (i < lines.length && !isBlank(lines[i]) && lines[i].includes('|')) rows.push(cells(lines[i++]));
				const st = (k: number) => (align[k] ? ` style="text-align:${align[k]}"` : '');
				out.push(
					`<div class="table"><table><thead><tr>${head.map((c, k) => `<th${st(k)}>${inline(c, this.opt)}</th>`).join('')}</tr></thead><tbody>` +
						rows.map((r) => `<tr>${head.map((_, k) => `<td${st(k)}>${inline(r[k] ?? '', this.opt)}</td>`).join('')}</tr>`).join('') +
						'</tbody></table></div>'
				);
				continue;
			}
			const para: string[] = [];
			while (i < lines.length && !isBlank(lines[i]) && !(para.length > 0 && startsBlock(lines[i]))) {
				if (para.length > 0 && lines[i].includes('|') && reTableSep.test(lines[i + 1] ?? '')) break;
				para.push(lines[i++].trim());
			}
			out.push(`<p>${inline(para.join('\n'), this.opt)}</p>`);
		}
		return out.join('\n');
	}

	code(body: string, info: string): string {
		if (info.trim().split(/\s+/)[0] === 'tree') {
			return (
				`<div class="code tree"><div class="code-head"><span>tree</span>` +
				`<button type="button" class="copy" data-copy data-copy-text="${esc(treeText(body))}" aria-label="Copy file tree">copy</button></div>` +
				`<div class="tree-body" role="group" aria-label="File tree">${treeHTML(body)}</div></div>`
			);
		}
		const lang = language(info);
		const label = info.trim().split(/\s+/)[0] || 'text';
		return (
			`<div class="code"><div class="code-head"><span>${esc(label)}</span>` +
			`<button type="button" class="copy" data-copy aria-label="Copy code">copy</button></div>` +
			`<pre><code${lang ? ` data-lang="${lang}"` : ''}>${highlight(body, lang)}</code></pre></div>`
		);
	}

	quote(body: string[]): string {
		const m = body[0]?.trim().match(/^\[!(\w+)\]\s*(.*)$/);
		if (m && callouts[m[1].toUpperCase()]) {
			const kind = m[1].toLowerCase();
			const rest = [m[2], ...body.slice(1)].filter((l, k) => k > 0 || l !== '');
			return (
				`<aside class="callout callout-${kind}" role="note"><p class="callout-title">${callouts[m[1].toUpperCase()]}</p>` +
				`${this.blocks(rest, false)}</aside>`
			);
		}
		return `<blockquote>${this.blocks(body, false)}</blockquote>`;
	}

	list(lines: string[], indent: number): string {
		const first = lines[0].match(reItem)!;
		const ordered = /\d/.test(first[2]);
		const startN = ordered ? parseInt(first[2], 10) : 1;
		const items: string[][] = [];
		let loose = false;
		for (const l of lines) {
			const m = l.match(reItem);
			if (m && m[1].length === indent) {
				items.push([m[3]]);
			} else {
				const cur = items[items.length - 1];
				if (l.trim() === '') {
					cur.push('');
				} else {
					if (cur[cur.length - 1] === '' && !reItem.test(l)) loose = true;
					cur.push(l.replace(new RegExp(`^\\s{0,${indent + 4}}`), ''));
				}
			}
		}
		const lis = items.map((it) => {
			while (it.length > 0 && it[it.length - 1] === '') it.pop();
			let html = this.blocks(it, false);
			if (!loose) html = html.replace(/^<p>([\s\S]*?)<\/p>/, '$1');
			return `<li>${html}</li>`;
		});
		const tag = ordered ? 'ol' : 'ul';
		return `<${tag}${ordered && startN !== 1 ? ` start="${startN}"` : ''}>${lis.join('')}</${tag}>`;
	}
}

/** Renders markdown to HTML and lists its h2/h3 headings with their anchor ids. */
export function render(src: string, opt: Options = {}): Rendered {
	const r = new Renderer(opt);
	const html = r.blocks(src.replace(/\r\n?/g, '\n').split('\n'), true);
	return { html, headings: r.headings };
}

export type Frontmatter = Record<string, string>;

/** Splits "---\nkey: value\n---\nbody" into flat string fields and the body. */
export function frontmatter(raw: string): { data: Frontmatter; body: string } {
	const m = raw.replace(/\r\n?/g, '\n').match(/^---\n([\s\S]*?)\n---\n?([\s\S]*)$/);
	if (!m) return { data: {}, body: raw };
	const data: Frontmatter = {};
	for (const line of m[1].split('\n')) {
		const kv = line.match(/^([A-Za-z_][\w-]*)\s*:\s*(.*)$/);
		if (!kv) continue;
		let v = kv[2].trim();
		if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) v = v.slice(1, -1);
		data[kv[1]] = v;
	}
	return { data, body: m[2] };
}
