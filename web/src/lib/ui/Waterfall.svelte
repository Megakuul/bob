<script lang="ts">
	// Timing waterfall: one bar per started package, positioned on the build's time axis; a
	// running package extends to now.
	import type { Span } from '$lib/graph';

	let { spans }: { spans: Span[] } = $props();

	const axis = $derived.by(() => {
		const now = Date.now();
		const t0 = Math.min(...spans.map((s) => s.start));
		const t1 = Math.max(...spans.map((s) => s.end ?? now));
		return { t0, len: Math.max(1, t1 - t0), now };
	});
	const sorted = $derived([...spans].sort((a, b) => a.start - b.start));

	const look: Record<string, string> = {
		BUILDING: 'bg-accent',
		FAILED: 'bg-fail',
		CACHED: 'bg-idle',
		SUCCEEDED: 'bg-ok'
	};
</script>

<div class="flex flex-col gap-0.5 border border-line p-2">
	{#each sorted as s (s.label)}
		{@const left = ((s.start - axis.t0) / axis.len) * 100}
		{@const width = (((s.end ?? axis.now) - s.start) / axis.len) * 100}
		<div class="grid grid-cols-[minmax(8rem,16rem)_1fr] items-center gap-2">
			<span class="truncate font-mono text-sm" title={s.label}>{s.label}</span>
			<div class="relative h-3">
				<div
					data-bar={s.label}
					title="{s.label} {s.state}"
					class="absolute inset-y-0 min-w-0.5 {look[s.state] ?? 'bg-idle'}"
					style="left: {left}%; width: {width}%"
				></div>
			</div>
		</div>
	{/each}
</div>
