<script lang="ts">
	// Tabs are links that set ?tab=; the first tab is the default and has no parameter.
	import { page } from '$app/state';

	let { tabs }: { tabs: { id: string; label: string }[] } = $props();
	const current = $derived(page.url.searchParams.get('tab') ?? tabs[0]?.id);
</script>

<nav class="flex overflow-x-auto border-b border-line">
	{#each tabs as t, i (t.id)}
		<a
			href={i === 0 ? page.url.pathname : `?tab=${t.id}`}
			class="-mb-px border-b-2 px-3 py-1.5 whitespace-nowrap {current === t.id
				? 'border-fg text-fg'
				: 'border-transparent text-muted hover:text-fg'}"
		>
			{t.label}
		</a>
	{/each}
</nav>
