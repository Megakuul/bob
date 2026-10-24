<script lang="ts">
	import type { Snippet } from 'svelte';

	let { num = false, children }: { num?: boolean; children?: Snippet } = $props();
</script>

{#if num}
	<th class="h-7 border-b border-line px-2 text-right font-medium whitespace-nowrap">{@render children?.()}</th>
{:else}
	<th class="h-7 border-b border-line px-2 font-medium whitespace-nowrap">{@render children?.()}</th>
{/if}
