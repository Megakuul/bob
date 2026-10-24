<script lang="ts">
	import Backdrop from './Backdrop.svelte';

	// The hero motif: a small build graph that replays one incremental build. One input
	// changed (api.proto); everything else comes from the cache, and the TypeScript client
	// rebuilds bit-identical, so the web app is an early-cutoff hit. Decorative: the same facts
	// are in the page text. Static (final state) without JS and with prefers-reduced-motion.
	type Kind = 'hit' | 'build' | 'cutoff';
	type Node = { id: string; label: string; short: string; target: string; col: number; start: number; dur: number; kind: Kind; deps: string[] };

	const nodes: Node[] = [
		{ id: 'ssl', label: 'nixpkgs openssl', short: 'openssl', target: '//third_party:openssl', col: 0, start: 1, dur: 0, kind: 'hit', deps: [] },
		{ id: 'crates', label: 'crates.io deps', short: 'crates', target: '//core:crates', col: 0, start: 1, dur: 0, kind: 'hit', deps: [] },
		{ id: 'gomod', label: 'go modules', short: 'go mod', target: '//:module', col: 0, start: 2, dur: 0, kind: 'hit', deps: [] },
		{ id: 'proto', label: 'api.proto', short: 'proto', target: '//proto:api', col: 0, start: 1, dur: 2, kind: 'build', deps: [] },
		{ id: 'npm', label: 'npm deps', short: 'npm', target: '//web:deps', col: 0, start: 2, dur: 0, kind: 'hit', deps: [] },
		{ id: 'libnet', label: 'libnet · c++', short: 'libnet', target: '//net:libnet', col: 1, start: 3, dur: 0, kind: 'hit', deps: ['ssl'] },
		{ id: 'libcore', label: 'libcore · rust', short: 'libcore', target: '//core:libcore', col: 1, start: 3, dur: 0, kind: 'hit', deps: ['crates'] },
		{ id: 'conngo', label: 'connect-go', short: 'gen go', target: '//proto:connect-go', col: 1, start: 3, dur: 3, kind: 'build', deps: ['proto'] },
		{ id: 'connes', label: 'connect-es', short: 'gen ts', target: '//proto:connect-es', col: 1, start: 3, dur: 3, kind: 'build', deps: ['proto'] },
		{ id: 'api', label: 'api · go + cgo', short: 'api', target: '//cmd/api:api', col: 2, start: 6, dur: 4, kind: 'build', deps: ['libnet', 'libcore', 'gomod', 'conngo'] },
		{ id: 'web', label: 'web · svelte', short: 'web', target: '//web:app', col: 2, start: 6, dur: 0, kind: 'cutoff', deps: ['connes', 'npm'] },
		{ id: 'image', label: 'image · oci', short: 'image', target: '//deploy:image', col: 3, start: 10, dur: 2, kind: 'build', deps: ['api', 'web'] },
		{ id: 'release', label: 'release v1.4.0', short: 'release', target: '//deploy:release', col: 4, start: 12, dur: 2, kind: 'build', deps: ['image'] }
	];
	const byId = new Map(nodes.map((n) => [n.id, n]));
	const cols = [0, 1, 2, 3, 4].map((c) => nodes.filter((n) => n.col === c));

	const END = 14; // every node done
	const CYCLE = 26; // steps until the replay starts again
	const TICK = 360; // ms per step

	let step = $state(END);

	$effect(() => {
		if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
		let timer: ReturnType<typeof setInterval> | undefined;
		const hold = setTimeout(() => {
			step = 0;
			timer = setInterval(() => {
				if (!document.hidden) step = (step + 1) % CYCLE;
			}, TICK);
		}, 2200);
		return () => {
			clearTimeout(hold);
			clearInterval(timer);
		};
	});

	type St = 'idle' | 'building' | 'done';
	const stateOf = (n: Node, s: number): St => (s < n.start ? 'idle' : s < n.start + n.dur ? 'building' : 'done');
	const states = $derived(new Map(nodes.map((n) => [n.id, stateOf(n, step)])));
	const status = (n: Node, st: St) =>
		st === 'idle' ? 'waiting' : st === 'building' ? 'building' : n.kind === 'hit' ? 'cache hit' : n.kind === 'cutoff' ? 'early cutoff' : 'built';

	const done = $derived(nodes.filter((n) => states.get(n.id) === 'done'));
	const hits = $derived(done.filter((n) => n.kind !== 'build').length);
	const built = $derived(done.length - hits);
	const log = $derived(
		done
			.toSorted((a, b) => a.start + a.dur - (b.start + b.dur))
			.slice(-4)
			.map((n) => ({
				word: n.kind === 'hit' ? 'hit' : n.kind === 'cutoff' ? 'cutoff' : 'built',
				target: n.target,
				note: n.kind === 'cutoff' ? 'rebuilt inputs were bit-identical' : n.kind === 'build' ? `${(n.dur * 0.6 + 0.3).toFixed(1)}s` : 'from cache'
			}))
	);

	// Two layouts: left-to-right for wide screens, top-to-bottom (short labels) for phones.
	type Box = { x: number; y: number; w: number; h: number };
	const wide = { W: 1000, H: 352, w: 162, h: 44 };
	const narrow = { W: 400, H: 500, w: 72, h: 40 };
	function boxWide(n: Node): Box {
		const col = cols[n.col];
		const gap = 66;
		const top = (wide.H - (col.length - 1) * gap - wide.h) / 2;
		return { x: 8 + n.col * ((wide.W - 16 - wide.w) / 4), y: top + col.indexOf(n) * gap, w: wide.w, h: wide.h };
	}
	function boxNarrow(n: Node): Box {
		const row = cols[n.col];
		const gap = 76;
		const left = (narrow.W - (row.length - 1) * gap - narrow.w) / 2;
		return { x: left + row.indexOf(n) * gap, y: 6 + n.col * 112, w: narrow.w, h: narrow.h };
	}
	const edges = nodes.flatMap((n) => n.deps.map((d) => ({ from: byId.get(d)!, to: n })));
	function pathWide(a: Box, b: Box) {
		const x1 = a.x + a.w, y1 = a.y + a.h / 2, x2 = b.x, y2 = b.y + b.h / 2, m = (x2 - x1) / 2;
		return `M${x1} ${y1}C${x1 + m} ${y1} ${x2 - m} ${y2} ${x2} ${y2}`;
	}
	function pathNarrow(a: Box, b: Box) {
		const x1 = a.x + a.w / 2, y1 = a.y + a.h, x2 = b.x + b.w / 2, y2 = b.y, m = (y2 - y1) / 2;
		return `M${x1} ${y1}C${x1} ${y1 + m} ${x2} ${y2 - m} ${x2} ${y2}`;
	}
	const edgeClass = (from: Node, to: Node) => {
		const f = states.get(from.id), t = states.get(to.id);
		if (f !== 'done') return 'stroke-line';
		return t === 'building' ? 'stroke-wire flow' : 'stroke-wire';
	};
</script>

{#snippet graph(layout: typeof wide, box: (n: Node) => Box, path: (a: Box, b: Box) => string, short: boolean)}
	<svg viewBox="0 0 {layout.W} {layout.H}" class="relative block h-auto w-full" aria-hidden="true">
		{#each edges as e (e.from.id + e.to.id)}
			<path d={path(box(e.from), box(e.to))} fill="none" stroke-width="1.5" class="edge {edgeClass(e.from, e.to)}" />
		{/each}
		{#each nodes as n (n.id)}
			{@const b = box(n)}
			{@const st = states.get(n.id) ?? 'idle'}
			{@const filled = st === 'done' && n.kind === 'build'}
			<g class="node {st}" transform="translate({b.x} {b.y})">
				<rect
					x="0.5"
					y="0.5"
					width={b.w - 1}
					height={b.h - 1}
					rx="2"
					stroke-width={st === 'idle' ? 1 : 1.5}
					class={filled ? 'fill-accent stroke-accent' : st === 'idle' ? 'fill-bg stroke-line' : 'fill-surface stroke-accent'}
				/>
				{#if st === 'done' && !filled}
					<rect x={short ? 4 : 10} y={short ? 4 : 11} width={short ? 5 : 6} height={short ? 5 : 6} class="fill-accent" />
				{/if}
				{#if short}
					<text x={b.w / 2} y={b.h / 2 + 4.5} text-anchor="middle" font-size="13" class="font-mono {filled ? 'fill-on-accent' : st === 'idle' ? 'fill-muted' : 'fill-fg'}">{n.short}</text>
				{:else}
					<text x={st === 'done' && !filled ? 22 : 10} y="18" font-size="13" font-weight="600" class="font-mono {filled ? 'fill-on-accent' : st === 'idle' ? 'fill-muted' : 'fill-fg'}">{n.label}</text>
					<text x="10" y="35" font-size="11" class="font-mono {filled ? 'fill-on-accent' : 'fill-muted'}">{status(n, st)}</text>
				{/if}
			</g>
		{/each}
	</svg>
{/snippet}

<figure class="border border-line bg-bg" aria-label="An incremental build replayed as a graph: one changed input, cache hits and an early cutoff">
	<div class="flex flex-wrap items-center justify-between gap-x-6 gap-y-1 border-b border-line px-3 py-2 font-mono text-sm text-muted sm:px-4">
		<span><span class="text-fg">$ bob build //deploy:release</span> · linux/arm64</span>
		<span aria-hidden="true">
			<span class="text-fg">{hits}</span> cached · <span class="text-fg">{built}</span> built · <span class="text-fg">{nodes.length - done.length}</span> pending
		</span>
	</div>
	<div class="relative px-2 py-4 sm:px-4 sm:py-6">
		<Backdrop />
		<div class="hidden sm:block">{@render graph(wide, boxWide, pathWide, false)}</div>
		<div class="sm:hidden">{@render graph(narrow, boxNarrow, pathNarrow, true)}</div>
	</div>
	<ol class="h-[92px] overflow-hidden border-t border-line px-3 py-2 font-mono text-sm leading-[19px] sm:px-4" aria-hidden="true">
		{#each log as l (l.target)}
			<li class="flex gap-3 whitespace-nowrap">
				<span class="w-12 shrink-0 {l.word === 'built' ? 'text-fg' : 'text-muted'}">{l.word}</span>
				<span class="truncate text-fg">{l.target}</span>
				<span class="hidden text-muted sm:inline">{l.note}</span>
			</li>
		{/each}
	</ol>
	<div class="flex items-center gap-4 border-t border-line px-3 py-2 font-mono text-xs text-muted sm:px-4" aria-hidden="true">
		<span class="flex items-center gap-1.5"><i class="inline-block h-2.5 w-2.5 border border-line"></i>waiting</span>
		<span class="flex items-center gap-1.5"><i class="inline-block h-2.5 w-2.5 border border-accent"></i>building</span>
		<span class="flex items-center gap-1.5"><i class="inline-block h-2.5 w-2.5 bg-accent"></i>built</span>
		<span class="flex items-center gap-1.5"><i class="inline-block h-2.5 w-2.5 border border-accent bg-surface"></i>cache hit</span>
	</div>
</figure>

<style>
	.node rect {
		transition:
			fill 0.25s,
			stroke 0.25s;
	}
	.building rect:first-child {
		stroke-dasharray: 5 4;
		animation: march 0.5s linear infinite;
	}
	.edge {
		transition: stroke 0.3s;
	}
	.flow {
		stroke-dasharray: 4 6;
		animation: march 0.5s linear infinite;
	}
	@keyframes march {
		to {
			stroke-dashoffset: -9;
		}
	}
</style>
