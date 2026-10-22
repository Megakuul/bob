<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		variant = 'secondary',
		href,
		type = 'button',
		disabled = false,
		name,
		value,
		onclick,
		children
	}: {
		variant?: 'primary' | 'secondary' | 'danger';
		href?: string;
		type?: 'button' | 'submit';
		disabled?: boolean;
		name?: string;
		value?: string;
		onclick?: (e: MouseEvent) => void;
		children: Snippet;
	} = $props();

	const look = {
		primary: 'border-accent bg-accent text-on-accent hover:border-fg',
		secondary: 'border-line bg-bg text-fg hover:bg-surface',
		danger: 'border-fail bg-bg text-fail hover:bg-surface'
	};
</script>

{#if href}
	<a {href} class="inline-flex h-7 items-center rounded-xs border px-3 text-base whitespace-nowrap {look[variant]}">
		{@render children()}
	</a>
{:else}
	<button
		{type}
		{name}
		{value}
		{disabled}
		{onclick}
		class="inline-flex h-7 items-center rounded-xs border px-3 text-base whitespace-nowrap disabled:cursor-not-allowed disabled:opacity-50 {look[variant]}"
	>
		{@render children()}
	</button>
{/if}
