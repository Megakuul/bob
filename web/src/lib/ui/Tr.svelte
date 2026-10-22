<script lang="ts">
	import type { Snippet } from 'svelte';

	// selected rows get the 2px yellow left bar (SPEC §6.10.2).
	let { selected = false, onclick, children }: {
		selected?: boolean;
		onclick?: () => void;
		children: Snippet;
	} = $props();
</script>

<tr
	{onclick}
	class="hover:bg-surface [&:last-child>td]:border-b-0 {selected
		? 'bg-surface [&>td:first-child]:border-l-2 [&>td:first-child]:border-l-accent'
		: ''}"
	class:cursor-pointer={!!onclick}
>
	{@render children()}
</tr>
