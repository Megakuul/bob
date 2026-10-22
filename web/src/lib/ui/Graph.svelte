<script lang="ts">
	// Layered left-to-right package graph (SPEC §6.10.3): longest-path layering, dependencies
	// to the left of their dependents. Plain SVG, no layout library.
	import type { GraphNode } from '$lib/graph';

	let { nodes, onselect }: { nodes: GraphNode[]; onselect?: (label: string) => void } = $props();

	const w = 200;
	const h = 24;
	const gx = 48;
	const gy = 8;

	const layout = $derived.by(() => {
		const layer: number[] = new Array(nodes.length).fill(-1);
		const depth = (i: number, seen: Set<number>): number => {
			if (layer[i] >= 0) return layer[i];
			if (seen.has(i)) return 0; // a cycle cannot come from the server; never loop on one
			seen.add(i);
			let d = 0;
			for (const j of nodes[i].deps) if (j >= 0 && j < nodes.length) d = Math.max(d, depth(j, seen) + 1);
			return (layer[i] = d);
		};
		const rows: number[] = [];
		const pos = nodes.map((_, i) => {
			const x = depth(i, new Set());
			const y = (rows[x] = (rows[x] ?? -1) + 1);
			return { x: x * (w + gx), y: y * (h + gy) };
		});
		return {
			pos,
			width: Math.max(1, rows.length) * (w + gx) - gx,
			height: Math.max(1, ...rows.map((r) => r + 1)) * (h + gy) - gy
		};
	});

	const look: Record<string, string> = {
		run: 'fill-accent stroke-accent',
		ok: 'fill-bg stroke-ok',
		fail: 'fill-bg stroke-fail',
		idle: 'fill-bg stroke-idle'
	};

	function tone(state: string): string {
		if (state === 'BUILDING') return 'run';
		if (state === 'SUCCEEDED' || state === 'CACHED') return 'ok';
		if (state === 'FAILED') return 'fail';
		return 'idle';
	}

	const short = (s: string) => (s.length > 30 ? '…' + s.slice(-29) : s);
</script>

<div class="overflow-auto border border-line p-3">
	<svg width={layout.width} height={layout.height} class="font-mono text-xs" role="img" aria-label="package graph">
		{#each nodes as n, i (i)}
			{#each n.deps as j (j)}
				{#if layout.pos[j]}
					<path
						d="M{layout.pos[j].x + w} {layout.pos[j].y + h / 2} C{layout.pos[j].x + w + gx / 2} {layout.pos[j].y + h / 2}, {layout.pos[i].x - gx / 2} {layout.pos[i].y + h / 2}, {layout.pos[i].x} {layout.pos[i].y + h / 2}"
						class="fill-none stroke-line"
					/>
				{/if}
			{/each}
		{/each}
		{#each nodes as n, i (i)}
			<g data-node={n.label} transform="translate({layout.pos[i].x} {layout.pos[i].y})" class="cursor-pointer" onclick={() => onselect?.(n.label)} role="button" tabindex="0" onkeydown={(e) => e.key === 'Enter' && onselect?.(n.label)}>
				<title>{n.label} {n.state}</title>
				<rect width={w} height={h} rx="2" class={look[tone(n.state)]} />
				<text x="8" y="16" class={tone(n.state) === 'run' ? 'fill-on-accent' : 'fill-fg'}>{short(n.label)}</text>
			</g>
		{/each}
	</svg>
</div>
