<script lang="ts">
	let { checked = $bindable(false), label }: { checked?: boolean; label: string } = $props();
</script>

<label class="inline-flex items-center gap-1.5 text-base">
	<input type="checkbox" bind:checked class="size-3.5 accent-fg" />
	{label}
</label>
