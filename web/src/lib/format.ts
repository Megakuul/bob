// Display formatting shared by every page. Times are relative to Date.now(), which the UI
// tests pin to the seed clock.
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';

export function date(ts: Timestamp | undefined): Date | undefined {
	return ts ? timestampDate(ts) : undefined;
}

// ago renders the age of ts as 45s, 2m, 3h or 4d; '' when unset.
export function ago(ts: Timestamp | undefined): string {
	const d = date(ts);
	if (!d) return '';
	const s = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000));
	if (s < 60) return s + 's';
	if (s < 3600) return Math.floor(s / 60) + 'm';
	if (s < 86400) return Math.floor(s / 3600) + 'h';
	return Math.floor(s / 86400) + 'd';
}

// duration renders milliseconds as m:ss or h:mm:ss.
export function duration(ms: number): string {
	if (!(ms >= 0)) return '';
	const t = Math.round(ms / 1000);
	const h = Math.floor(t / 3600);
	const m = Math.floor((t % 3600) / 60);
	const s = String(t % 60).padStart(2, '0');
	return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`;
}

// span is the duration between two timestamps; a running span ends now.
export function span(start: Timestamp | undefined, end: Timestamp | undefined): string {
	const a = date(start);
	if (!a) return '';
	const b = date(end)?.getTime() ?? Date.now();
	return duration(b - a.getTime());
}

const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];

export function bytes(n: bigint | number): string {
	let v = Number(n);
	let i = 0;
	while (v >= 1024 && i < units.length - 1) {
		v /= 1024;
		i++;
	}
	return i === 0 ? `${v} B` : `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

export function percent(rate: number): string {
	return Math.round(rate * 100) + '%';
}

// enumName is the short name of a generated proto enum value ("RUNNING"), '' when unspecified.
export function enumName(e: Record<number, string>, v: number): string {
	return v ? (e[v] ?? String(v)) : '';
}

// shortRef turns refs/heads/main into main, refs/tags/v1 into v1, refs/pull/88/head into
// pr/88 and refs/merge-requests/7/head into mr/7.
export function shortRef(ref: string): string {
	return ref
		.replace(/^refs\/(heads|tags)\//, '')
		.replace(/^refs\/pull\/(\d+)\/head$/, 'pr/$1')
		.replace(/^refs\/merge-requests\/(\d+)\/head$/, 'mr/$1');
}

// matches is the client-side text filter of every list (R-UI-006): the lower-cased query is
// contained in one of the row's id or name fields.
export function matches(query: string, ...fields: (string | undefined)[]): boolean {
	const q = query.trim().toLowerCase();
	return !q || fields.some((f) => f?.toLowerCase().includes(q));
}
