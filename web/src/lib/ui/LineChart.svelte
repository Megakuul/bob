<script lang="ts">
	// Daily lines (e.g. p50/p90 duration), hand-written SVG.
	type Series = { key: string; label: string; dashed?: boolean };
	let { series, rows, unit = '', height = 140 }: {
		series: Series[];
		rows: { x: string; v: Record<string, number> }[];
		unit?: string;
		height?: number;
	} = $props();

	const max = $derived(Math.max(1, ...rows.flatMap((r) => series.map((s) => r.v[s.key] ?? 0))));
	const w = 1000;
	const pad = 4;
	const path = (key: string) =>
		rows
			.map((r, i) => {
				const x = rows.length < 2 ? w / 2 : (i / (rows.length - 1)) * w;
				const y = height - pad - ((r.v[key] ?? 0) / max) * (height - 2 * pad);
				return `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`;
			})
			.join(' ');
</script>

<figure class="flex flex-col gap-1">
	<div class="flex gap-4 text-sm text-muted">
		{#each series as s (s.key)}
			<span class="inline-flex items-center gap-1">
				<svg width="16" height="6" aria-hidden="true"><line x1="0" y1="3" x2="16" y2="3" class="stroke-fg" stroke-width="2" stroke-dasharray={s.dashed ? '4 3' : undefined} /></svg>{s.label}
			</span>
		{/each}
		<span class="ml-auto tabular-nums">max {Math.round(max)}{unit}</span>
	</div>
	<svg viewBox="0 0 {w} {height}" preserveAspectRatio="none" class="h-[140px] w-full border-b border-line" role="img" aria-label="per day">
		{#each series as s (s.key)}
			<path d={path(s.key)} class="fill-none stroke-fg" stroke-width="2" vector-effect="non-scaling-stroke" stroke-dasharray={s.dashed ? '6 4' : undefined} />
		{/each}
	</svg>
	{#if rows.length}
		<div class="flex justify-between text-xs text-muted tabular-nums"><span>{rows[0].x}</span><span>{rows[rows.length - 1].x}</span></div>
	{/if}
</figure>
