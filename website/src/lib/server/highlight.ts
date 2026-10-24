// A small hand-written syntax highlighter for the languages the docs use. It knows comments,
// strings, numbers and keywords (go, bash, yaml, json, proto, ts, nix), nothing more: light highlighting on the existing palette
// (classes t-k keyword, t-s string, t-c comment, t-n number/variable, t-p punctuation/prompt).
// Input is raw source; output is escaped HTML.

type Rule = { re: RegExp; cls: string | ((m: string) => string) };

const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

function words(list: string): Set<string> {
	return new Set(list.split(/\s+/).filter(Boolean));
}

const goKw = words(`break case chan const continue default defer else fallthrough for func go goto if
	import interface map package range return select struct switch type var nil true false iota`);
const tsKw = words(`as async await break case catch class const continue default delete do else export
	extends false finally for from function if import in instanceof interface let new null of return
	static super switch this throw true try type typeof undefined var void while yield`);
const protoKw = words(`syntax package import option message service rpc returns stream enum repeated
	optional required map oneof reserved extend true false string bytes bool int32 int64 uint32 uint64
	sint32 sint64 fixed32 fixed64 sfixed32 sfixed64 float double`);
const nixKw = words(`let in with inherit rec if then else assert import true false null`);
const shKw = words(`if then else elif fi for in do done while until case esac function return export
	local set unset cd echo exit`);

const ident = (kw: Set<string>): Rule => ({
	re: /[A-Za-z_][\w]*/y,
	cls: (m) => (kw.has(m) ? 't-k' : '')
});
const num: Rule = { re: /\b\d[\d_]*(\.\d+)?([eE][+-]?\d+)?\b|\b0x[0-9a-fA-F]+\b/y, cls: 't-n' };
const dq: Rule = { re: /"(?:[^"\\\n]|\\.)*"?/y, cls: 't-s' };
const sq: Rule = { re: /'(?:[^'\\\n]|\\.)*'?/y, cls: 't-s' };
const bt: Rule = { re: /`[^`]*`?/y, cls: 't-s' };
const lineC: Rule = { re: /\/\/[^\n]*/y, cls: 't-c' };
const blockC: Rule = { re: /\/\*[\s\S]*?(?:\*\/|$)/y, cls: 't-c' };
const hashC: Rule = { re: /(?<=^|\s)#[^\n]*/y, cls: 't-c' };

const langs: Record<string, Rule[]> = {
	go: [lineC, blockC, dq, bt, sq, num, ident(goKw)],
	ts: [lineC, blockC, dq, sq, bt, num, ident(tsKw)],
	proto: [lineC, blockC, dq, sq, num, ident(protoKw)],
	bash: [
		{ re: /(?<=^|\n)\$ /y, cls: 't-p' },
		hashC,
		dq,
		sq,
		{ re: /\$\{[^}\n]*\}|\$[A-Za-z_]\w*|\$[0-9@#?*!$]/y, cls: 't-n' },
		{ re: /[A-Za-z_][\w-]*/y, cls: (m) => (shKw.has(m) ? 't-k' : '') }
	],
	nix: [
		hashC,
		blockC,
		{ re: /"(?:[^"\\]|\\.|\$\{[^}]*\})*"?|''[\s\S]*?(?:''|$)/y, cls: 't-s' },
		num,
		{ re: /[A-Za-z_][\w'-]*/y, cls: (m) => (nixKw.has(m) ? 't-k' : '') }
	],
	yaml: [
		hashC,
		{ re: /(?<=^|\n)[ \t]*-?[ \t]*[\w.\-/"']+(?=:(\s|$))/y, cls: 't-k' },
		dq,
		sq,
		{ re: /\b(true|false|null|yes|no)\b/y, cls: 't-n' },
		num
	],
	json: [
		{ re: /"(?:[^"\\\n]|\\.)*"(?=\s*:)/y, cls: 't-k' },
		dq,
		{ re: /\b(true|false|null)\b/y, cls: 't-n' },
		{ re: /-?\b\d+(\.\d+)?([eE][+-]?\d+)?\b/y, cls: 't-n' }
	]
};

const aliases: Record<string, string> = {
	golang: 'go',
	typescript: 'ts',
	javascript: 'ts',
	js: 'ts',
	tsx: 'ts',
	svelte: 'ts',
	sh: 'bash',
	shell: 'bash',
	console: 'bash',
	zsh: 'bash',
	yml: 'yaml',
	jsonc: 'json',
	protobuf: 'proto'
};

/** Canonical language name for a fence info string ('' when unknown). */
export function language(info: string): string {
	const l = info.trim().split(/\s+/)[0]?.toLowerCase() ?? '';
	const name = aliases[l] ?? l;
	return name in langs ? name : '';
}

/** Highlights code in lang (a name from language()); unknown languages are only escaped. */
export function highlight(code: string, lang: string): string {
	const rules = langs[lang];
	if (!rules) return esc(code);
	let out = '';
	let plain = '';
	let i = 0;
	const flush = () => {
		out += esc(plain);
		plain = '';
	};
	outer: while (i < code.length) {
		for (const r of rules) {
			r.re.lastIndex = i;
			const m = r.re.exec(code);
			if (m && m[0].length > 0) {
				const cls = typeof r.cls === 'string' ? r.cls : r.cls(m[0]);
				if (cls) {
					flush();
					out += `<span class="${cls}">${esc(m[0])}</span>`;
				} else {
					plain += m[0];
				}
				i += m[0].length;
				continue outer;
			}
		}
		plain += code[i++];
	}
	flush();
	return out;
}
