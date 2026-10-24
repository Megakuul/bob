// ANSI SGR colours mapped to theme tokens (SPEC §6.10.1). Every other escape sequence is
// dropped; the text itself is only ever inserted as text nodes.
export type Segment = { text: string; tone: Tone; bold: boolean };
export type Tone = '' | 'fail' | 'ok' | 'warn' | 'muted';

// CSI sequences (ESC [ … final byte) and the two-byte ESC forms.
const escape = /\x1b(?:\[([0-9;?]*)([@-~])|[@-Z\\-_])/g;

function tone(code: number, cur: Tone): Tone {
	switch (code) {
		case 31:
		case 91:
			return 'fail';
		case 32:
		case 92:
			return 'ok';
		case 33:
		case 93:
			return 'warn';
		case 90:
			return 'muted';
		case 39:
			return '';
	}
	return code >= 30 && code <= 37 ? '' : code >= 94 && code <= 97 ? '' : cur;
}

export function parse(line: string): Segment[] {
	if (!line.includes('\x1b')) return [{ text: line, tone: '', bold: false }];
	const out: Segment[] = [];
	let cur: Tone = '';
	let bold = false;
	let last = 0;
	for (const m of line.matchAll(escape)) {
		if (m.index > last) out.push({ text: line.slice(last, m.index), tone: cur, bold });
		last = m.index + m[0].length;
		if (m[2] !== 'm') continue;
		for (const p of (m[1] || '0').split(';')) {
			const n = Number(p);
			if (n === 0) {
				cur = '';
				bold = false;
			} else if (n === 1) bold = true;
			else if (n === 22) bold = false;
			else cur = tone(n, cur);
		}
	}
	if (last < line.length) out.push({ text: line.slice(last), tone: cur, bold });
	return out;
}
