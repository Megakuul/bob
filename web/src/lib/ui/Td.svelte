<script lang="ts">
	import type { Snippet } from 'svelte';

	// num: right-aligned tabular figures; mono: ids, hashes, labels, store paths.
	let { num = false, mono = false, muted = false, colspan, children }: {
		num?: boolean;
		mono?: boolean;
		muted?: boolean;
		colspan?: number;
		children?: Snippet;
	} = $props();
</script>

<td
	{colspan}
	class="h-7 border-b border-line px-2 whitespace-nowrap"
	class:text-right={num}
	class:tabular-nums={num}
	class:font-mono={mono}
	class:text-sm={mono}
	class:text-muted={muted}
>
	{@render children?.()}
</td>
