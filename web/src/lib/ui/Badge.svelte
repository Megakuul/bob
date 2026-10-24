<script lang="ts">
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

<span class="inline-block rounded-xs border border-line px-1.5 text-xs leading-4 text-muted">{@render children()}</span>
