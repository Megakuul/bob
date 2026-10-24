<script lang="ts">
	let { rows }: { rows: { k: string; v: string; mono?: boolean }[] } = $props();
</script>

<dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1">
	{#each rows as r (r.k)}
		<dt class="text-muted">{r.k}</dt>
		<dd class="min-w-0 truncate" class:font-mono={r.mono} class:text-sm={r.mono}>{r.v || '—'}</dd>
	{/each}
</dl>
