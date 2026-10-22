<script lang="ts">
	import type { Snippet } from 'svelte';
	import EmptyState from './EmptyState.svelte';

	// count is the number of rows children renders; with 0 the table shows `empty` below the
	// header instead of a body row, so tbody holds data rows only.
	let { head, children, count, empty = 'Nothing here.' }: {
		head: Snippet;
		children: Snippet;
		count: number;
		empty?: string;
	} = $props();
</script>

<div class="overflow-x-auto border border-line">
	<table class="w-full border-collapse text-base">
		<thead class="bg-surface text-left text-sm text-muted">
			<tr>{@render head()}</tr>
		</thead>
		<tbody>{@render children()}</tbody>
	</table>
	{#if count === 0}
		<EmptyState text={empty} />
	{/if}
</div>
