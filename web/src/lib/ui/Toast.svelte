<script lang="ts">
	import { toast } from '$lib/toast.svelte';
</script>

{#if toast.text}
	<div
		role="status"
		class="fixed right-4 bottom-4 z-20 max-w-sm rounded-xs border px-3 py-2 {toast.error
			? 'border-fail bg-bg text-fail'
			: 'border-line bg-surface text-fg'}"
	>
		{toast.text}
	</div>
{/if}
