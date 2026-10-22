<script lang="ts">
	import { theme, type Choice } from '$lib/theme.svelte';

	const choices: { id: Choice; label: string }[] = [
		{ id: 'light', label: 'Light' },
		{ id: 'dark', label: 'Dark' },
		{ id: 'system', label: 'System' }
	];
</script>

<div class="inline-flex rounded-xs border border-line text-sm" role="group" aria-label="theme">
	{#each choices as c (c.id)}
		<button
			type="button"
			data-theme-choice={c.id}
			aria-pressed={theme.choice === c.id}
			onclick={() => theme.set(c.id)}
			class="px-2 py-0.5 {theme.choice === c.id ? 'bg-bg text-fg' : 'text-muted hover:text-fg'}"
		>
			{c.label}
		</button>
	{/each}
</div>
