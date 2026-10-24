// Client-side search over the index built at prerender time (titles, headings, summaries).
export type Entry = { title: string; section: string; path: string; summary: string; headings: { text: string; id: string }[] };
export type Hit = { href: string; title: string; context: string; score: number };

/** Every query word must occur; title matches rank above headings above summaries. */
export function search(index: Entry[], query: string, limit = 8): Hit[] {
	const words = query.toLowerCase().split(/\s+/).filter(Boolean);
	if (words.length === 0) return [];
	const has = (s: string) => {
		const l = s.toLowerCase();
		return words.every((w) => l.includes(w));
	};
	const hits: Hit[] = [];
	for (const e of index) {
		const page = `${e.section} ${e.title} ${e.summary}`;
		if (has(e.title)) hits.push({ href: e.path, title: e.title, context: e.section, score: 3 });
		else if (has(page)) hits.push({ href: e.path, title: e.title, context: e.summary || e.section, score: 1 });
		for (const h of e.headings) {
			if (has(`${e.title} ${h.text}`) && words.some((w) => h.text.toLowerCase().includes(w))) {
				hits.push({ href: `${e.path}#${h.id}`, title: h.text, context: e.title, score: 2 });
			}
		}
	}
	return hits.sort((a, b) => b.score - a.score).slice(0, limit);
}
