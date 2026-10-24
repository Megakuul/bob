<script lang="ts">
	import { theme } from '$lib/theme.svelte';

	$effect(() => theme.sync());
</script>

<button
	type="button"
	onclick={() => theme.toggle()}
	class="grid h-8 w-8 place-items-center rounded-xs border border-line text-muted hover:border-fg hover:text-fg"
	aria-label={theme.current === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
	title={theme.current === 'dark' ? 'Light theme' : 'Dark theme'}
>
	<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5">
		{#if theme.current === 'dark'}
			<circle cx="8" cy="8" r="3" />
			<path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.4 1.4M11.6 11.6L13 13M3 13l1.4-1.4M11.6 4.4L13 3" />
		{:else}
			<path d="M13 9.5A5.5 5.5 0 0 1 6.5 3a5.5 5.5 0 1 0 6.5 6.5z" />
		{/if}
	</svg>
</button>
