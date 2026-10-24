<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';

	let { value = $bindable(''), label, ...rest }: HTMLInputAttributes & { label?: string } = $props();
</script>

{#if label}
	<label class="flex flex-col gap-1 text-sm text-muted">
		{label}
		<input
			bind:value
			class="h-7 rounded-xs border border-line bg-bg px-2 text-base text-fg placeholder:text-muted"
			{...rest}
		/>
	</label>
{:else}
	<input
		bind:value
		class="h-7 rounded-xs border border-line bg-bg px-2 text-base text-fg placeholder:text-muted"
		{...rest}
	/>
{/if}
