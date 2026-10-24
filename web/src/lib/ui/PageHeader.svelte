<script lang="ts">
	import type { Snippet } from 'svelte';

	let { title, crumbs = [], meta, actions }: {
		title: string;
		crumbs?: { label: string; href: string }[];
		meta?: Snippet;
		actions?: Snippet;
	} = $props();
</script>

<header class="flex flex-col gap-1 border-b border-line pb-3">
	{#if crumbs.length}
		<nav class="flex flex-wrap gap-1 text-sm text-muted">
			{#each crumbs as c (c.href)}
				<a href={c.href} class="hover:text-fg hover:underline">{c.label}</a><span>/</span>
			{/each}
		</nav>
	{/if}
	<div class="flex flex-wrap items-center justify-between gap-2">
		<h1 class="min-w-0 truncate text-xl font-semibold">{title}</h1>
		{#if actions}
			<div class="flex flex-wrap gap-2">{@render actions()}</div>
		{/if}
	</div>
	{#if meta}
		<div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted">{@render meta()}</div>
	{/if}
</header>
