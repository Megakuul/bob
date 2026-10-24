<script lang="ts">
	// A 1px line pattern behind a panel: "grid" (blueprint squares) or "scan" (scanlines).
	// SVG patterns instead of CSS gradients (AGENTS.md §8 rules out gradients).
	let { kind = 'grid', size = 24 }: { kind?: 'grid' | 'scan'; size?: number } = $props();
	const id = $props.id();
</script>

<svg class="pointer-events-none absolute inset-0 h-full w-full {kind === 'grid' ? 'opacity-50' : ''}" aria-hidden="true">
	<defs>
		{#if kind === 'grid'}
			<pattern id="bd-{id}" width={size} height={size} patternUnits="userSpaceOnUse">
				<path d="M{size} 0.5H0.5V{size}" fill="none" stroke-width="1" class="stroke-line" />
			</pattern>
		{:else}
			<pattern id="bd-{id}" width="4" height="3" patternUnits="userSpaceOnUse">
				<rect width="4" height="1" class="fill-fg" opacity="0.035" />
			</pattern>
		{/if}
	</defs>
	<rect width="100%" height="100%" fill="url(#bd-{id})" />
</svg>
