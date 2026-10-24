<script lang="ts">
	import type { Snippet } from 'svelte';

	let { title, actions, children }: { title: string; actions?: Snippet; children: Snippet } = $props();
</script>

<section class="flex flex-col gap-2">
	<div class="flex min-h-7 flex-wrap items-center justify-between gap-2">
		<h2 class="text-lg font-semibold">{title}</h2>
		{@render actions?.()}
	</div>
	{@render children()}
</section>
