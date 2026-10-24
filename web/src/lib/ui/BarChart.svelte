<script lang="ts">
	// Stacked daily bars, hand-written SVG (no chart library, SPEC §6.10.1).
	type Series = { key: string; label: string; tone: 'ok' | 'fail' | 'idle' };
	let { series, rows, height = 140 }: { series: Series[]; rows: { x: string; v: Record<string, number> }[]; height?: number } =
		$props();

	const fill = { ok: 'fill-ok', fail: 'fill-fail', idle: 'fill-idle' };
	const swatch = { ok: 'bg-ok', fail: 'bg-fail', idle: 'bg-idle' };
	const total = (r: { v: Record<string, number> }) => series.reduce((s, x) => s + (r.v[x.key] ?? 0), 0);
	const max = $derived(Math.max(1, ...rows.map(total)));
	const w = 1000;
	const bw = $derived(w / Math.max(1, rows.length));
</script>

<figure class="flex flex-col gap-1">
	<div class="flex gap-4 text-sm text-muted">
		{#each series as s (s.key)}
			<span class="inline-flex items-center gap-1"><span class="inline-block size-2 {swatch[s.tone]}"></span>{s.label}</span>
		{/each}
		<span class="ml-auto tabular-nums">max {max}/day</span>
	</div>
	<svg viewBox="0 0 {w} {height}" preserveAspectRatio="none" class="h-[140px] w-full border-b border-line" role="img" aria-label="per day">
		{#each rows as r, i (r.x)}
			{@const parts = series.map((s) => ({ s, h: ((r.v[s.key] ?? 0) / max) * height }))}
			{#each parts as p, k (p.s.key)}
				{@const below = parts.slice(0, k).reduce((a, b) => a + b.h, 0)}
				<rect x={i * bw + bw * 0.15} width={bw * 0.7} y={height - below - p.h} height={p.h} class={fill[p.s.tone]}>
					<title>{r.x} {p.s.label} {r.v[p.s.key] ?? 0}</title>
				</rect>
			{/each}
		{/each}
	</svg>
	{#if rows.length}
		<div class="flex justify-between text-xs text-muted tabular-nums"><span>{rows[0].x}</span><span>{rows[rows.length - 1].x}</span></div>
	{/if}
</figure>
