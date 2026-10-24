<script lang="ts">
	let {
		value = $bindable(''),
		label,
		name,
		options,
		onchange
	}: {
		value?: string;
		label: string;
		name?: string;
		options: { value: string; label: string }[];
		onchange?: (value: string) => void;
	} = $props();
</script>

<label class="flex items-center gap-1 text-sm text-muted">
	{label}
	<select
		{name}
		bind:value
		onchange={() => onchange?.(value)}
		class="h-7 rounded-xs border border-line bg-bg px-1 text-base text-fg"
	>
		{#each options as o (o.value)}
			<option value={o.value}>{o.label}</option>
		{/each}
	</select>
</label>
