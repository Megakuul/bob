// ```tree blocks: an indented file listing with an optional leading emoji per line, e.g.
//   📁 cmd/
//     🐹 main.go      # the entry point
// Rendered with indentation guides, a fixed-width emoji column (so names align whatever width
// the emoji font gives each glyph) and muted "# comments". The copy text drops the emojis.

const esc = (s: string) =>
	s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

const reIcon = /^((?:\p{Extended_Pictographic}|\p{Regional_Indicator})(?:️|‍(?:\p{Extended_Pictographic}))*)\s*/u;

type Line = { depth: number; icon: string; name: string; comment: string };

function parse(src: string): Line[] {
	const raw = src.replace(/\t/g, '  ').split('\n').filter((l) => l.trim() !== '');
	const indents = raw.map((l) => l.match(/^ */)![0].length);
	const unit = Math.min(...indents.filter((n) => n > 0), Infinity);
	const base = Math.min(...indents);
	return raw.map((l, i) => {
		let rest = l.trim();
		const m = rest.match(reIcon);
		const icon = m ? m[1] : '';
		if (m) rest = rest.slice(m[0].length);
		const c = rest.match(/^(.*?)\s+#\s?(.*)$/);
		return {
			depth: unit === Infinity ? 0 : Math.round((indents[i] - base) / unit),
			icon,
			name: c ? c[1] : rest,
			comment: c ? c[2] : ''
		};
	});
}

/** Plain text of a tree (two spaces per level, no emojis, comments kept). */
export function treeText(src: string): string {
	return parse(src)
		.map((l) => '  '.repeat(l.depth) + l.name + (l.comment ? `  # ${l.comment}` : ''))
		.join('\n');
}

/** The tree's lines as HTML (the inside of a .tree-body element). */
export function treeHTML(src: string): string {
	return parse(src)
		.map((l) => {
			const guides = '<span class="tree-guide"></span>'.repeat(l.depth);
			const dir = l.name.endsWith('/');
			return (
				`<span class="tree-line">${guides}<span class="tree-icon" aria-hidden="true">${esc(l.icon)}</span>` +
				`<span class="${dir ? 'tree-dir' : 'tree-file'}">${esc(l.name)}</span>` +
				(l.comment ? `<span class="tree-note"># ${esc(l.comment)}</span>` : '') +
				'</span>'
			);
		})
		.join('');
}
