<script lang="ts">
	import type { Snippet } from 'svelte';

	// Numbered section heading: "02 / what" kicker, a big title, optional lead text.
	let { n, kicker, id, title, children }: { n: string; kicker: string; id: string; title: string; children?: Snippet } = $props();
</script>

<div class="flex flex-col gap-4">
	<p class="flex items-center gap-3 font-mono text-sm text-muted">
		<span class="bg-accent px-1.5 text-on-accent">{n}</span><span>/ {kicker}</span><span class="h-px flex-1 bg-line" aria-hidden="true"></span>
	</p>
	<h2 {id} class="max-w-[22ch] text-3xl leading-[1.1] font-bold tracking-tight md:text-4xl">{title}</h2>
	{#if children}<div class="max-w-[64ch] text-lg text-muted md:text-xl">{@render children()}</div>{/if}
</div>
