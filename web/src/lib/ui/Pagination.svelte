<script lang="ts">
	// Token pagination: the server hands out next tokens only, so "prev" walks a local stack.
	let { next, prev, onnext, onprev }: {
		next: string;
		prev: boolean;
		onnext: () => void;
		onprev: () => void;
	} = $props();
</script>

{#if next || prev}
	<div class="flex justify-end gap-3 text-sm">
		<button type="button" disabled={!prev} onclick={onprev} class="text-muted hover:text-fg disabled:opacity-40">‹ prev</button>
		<button type="button" disabled={!next} onclick={onnext} class="text-muted hover:text-fg disabled:opacity-40">next ›</button>
	</div>
{/if}
