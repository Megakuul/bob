<script lang="ts">
	import type { Snippet } from 'svelte';

	// Pages outside the app shell (login, device approval, OAuth consent): one centred box.
	let { children }: { children: Snippet } = $props();
</script>

<main class="flex min-h-screen flex-col items-center px-4 pt-[12vh]">
	<div class="mb-4 text-xl font-semibold">bob</div>
	<div class="w-full max-w-sm">
		{@render children()}
	</div>
</main>
