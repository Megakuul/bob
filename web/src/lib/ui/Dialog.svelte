<script lang="ts">
	import type { Snippet } from 'svelte';

	// Native <dialog>: focus trapping and Escape come from the browser.
	let { open = $bindable(false), title, children, actions }: {
		open?: boolean;
		title: string;
		children: Snippet;
		actions?: Snippet;
	} = $props();
	let el: HTMLDialogElement;

	$effect(() => {
		if (open && !el.open) el.showModal();
		if (!open && el.open) el.close();
	});
</script>

<dialog
	bind:this={el}
	onclose={() => (open = false)}
	class="m-auto w-[min(32rem,calc(100vw-2rem))] rounded-xs border border-line bg-bg p-0 text-fg backdrop:bg-fg/40"
>
	<div class="flex flex-col gap-3 p-4">
		<h2 class="text-lg font-semibold">{title}</h2>
		{@render children()}
		{#if actions}
			<div class="flex justify-end gap-2">{@render actions()}</div>
		{/if}
	</div>
</dialog>
