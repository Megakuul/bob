// The docs, read at build time from content/docs/<section>/<NN-page>.md (frontmatter: title,
// section, order, summary). Only server loads import this module, so the markdown never ships
// to the browser: prerendering turns it into HTML and a small search index.
import { frontmatter, render, type Heading } from './markdown';

export type DocMeta = {
	section: string; // section slug (directory name without an NN- prefix)
	sectionTitle: string;
	slug: string; // page slug (file name without NN- and .md)
	title: string;
	summary: string;
	order: number;
	path: string; // /docs/<section>/<slug>
};

export type Doc = DocMeta & { html: string; headings: Heading[] };

export type NavSection = { slug: string; title: string; pages: DocMeta[] };

export type SearchEntry = { title: string; section: string; path: string; summary: string; headings: { text: string; id: string }[] };

const raw = import.meta.glob('/content/docs/*/*.md', { query: '?raw', import: 'default', eager: true }) as Record<string, string>;

const strip = (name: string) => name.replace(/^\d+[-_]/, '');
const prefix = (name: string) => {
	const m = name.match(/^(\d+)[-_]/);
	return m ? Number(m[1]) : undefined;
};
const titleCase = (slug: string) => slug.replace(/[-_]+/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

// Section order when a directory has no NN- prefix.
const preferred = ['getting-started', 'start', 'guide', 'guides', 'concepts', 'ecosystems', 'outputs', 'server', 'reference'];

/** Rewrites links to markdown files into site paths; other links stay as written. */
function linker(section: string) {
	return (href: string): string => {
		const m = href.match(/^(?:\.\/)?(?:\.\.\/([^/#]+)\/)?([^/#]+)\.md(#.*)?$/);
		if (!m || /^[a-z]+:/i.test(href)) return href;
		return `/docs/${m[1] ? strip(m[1]) : section}/${strip(m[2])}${m[3] ?? ''}`;
	};
}

function load(): { docs: Doc[]; sections: NavSection[] } {
	const docs: (Doc & { dirOrder: number; file: string })[] = [];
	const parsed = Object.entries(raw).map(([file, text]) => ({ file, ...frontmatter(text) }));
	// Placeholder pages (frontmatter "placeholder: true") stand in only while no real page exists.
	const real = parsed.some((p) => p.data.placeholder !== 'true');
	for (const { file, data, body } of parsed) {
		if (real && data.placeholder === 'true') continue;
		const [dir, name] = file.split('/').slice(-2);
		if (!data.title) throw new Error(`${file}: frontmatter has no title`);
		const section = strip(dir);
		const slug = strip(name.replace(/\.md$/, ''));
		const order = Number(data.order ?? prefix(name) ?? 0);
		const { html, headings } = render(body, { link: linker(section), title: data.title });
		const pi = preferred.indexOf(section);
		docs.push({
			section,
			sectionTitle: data.section || titleCase(section),
			slug,
			title: data.title,
			summary: data.summary ?? '',
			order: Number.isFinite(order) ? order : 0,
			path: `/docs/${section}/${slug}`,
			html,
			headings,
			dirOrder: prefix(dir) ?? (pi >= 0 ? pi : 100),
			file
		});
	}
	docs.sort((a, b) => a.dirOrder - b.dirOrder || a.section.localeCompare(b.section) || a.order - b.order || a.file.localeCompare(b.file));
	const sections: NavSection[] = [];
	for (const d of docs) {
		let s = sections.find((x) => x.slug === d.section);
		if (!s) sections.push((s = { slug: d.section, title: d.sectionTitle, pages: [] }));
		s.pages.push(meta(d));
	}
	return { docs: docs.map(({ dirOrder: _o, file: _f, ...d }) => d), sections };
}

function meta(d: Doc): DocMeta {
	const { html: _h, headings: _hs, ...m } = d;
	return m;
}

const all = load();

export const docs = all.docs;
export const sections = all.sections;

export function find(section: string, slug: string): { doc: Doc; prev?: DocMeta; next?: DocMeta } | undefined {
	const i = docs.findIndex((d) => d.section === section && d.slug === slug);
	if (i < 0) return undefined;
	return {
		doc: docs[i],
		prev: i > 0 ? meta(docs[i - 1]) : undefined,
		next: i < docs.length - 1 ? meta(docs[i + 1]) : undefined
	};
}

export const searchIndex: SearchEntry[] = docs.map((d) => ({
	title: d.title,
	section: d.sectionTitle,
	path: d.path,
	summary: d.summary,
	headings: d.headings.map((h) => ({ text: h.text, id: h.id }))
}));
