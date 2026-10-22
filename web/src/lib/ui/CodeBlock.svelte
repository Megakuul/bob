<script lang="ts">
	import Mono from './Mono.svelte';

	let { text }: { text: string } = $props();
</script>

<div class="flex items-center justify-between gap-4 rounded-xs border border-line bg-surface px-3 py-2">
	<Mono {text} copy />
</div>
