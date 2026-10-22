<script lang="ts">
	// Bordered cells with a number and a label (SPEC §6.10.3: not cards with icons).
	let { cells }: { cells: { label: string; value: string }[] } = $props();
</script>

<div class="grid grid-cols-2 border-t border-l border-line md:grid-flow-col md:grid-cols-none md:auto-cols-fr">
	{#each cells as c (c.label)}
		<div class="border-r border-b border-line px-3 py-2">
			<div class="text-xl font-semibold tabular-nums">{c.value}</div>
			<div class="text-sm text-muted">{c.label}</div>
		</div>
	{/each}
</div>
