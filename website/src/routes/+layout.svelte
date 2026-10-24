<script lang="ts">
	import '../app.css';
	import type { Snippet } from 'svelte';
	import Footer from '$lib/ui/Footer.svelte';
	import Header from '$lib/ui/Header.svelte';

	let { children }: { children: Snippet } = $props();
</script>

<a href="#main" class="sr-only z-50 bg-accent px-3 py-2 font-mono text-sm text-on-accent focus:not-sr-only focus:fixed focus:top-2 focus:left-2">Skip to content</a>
<Header />
<main id="main" tabindex="-1" class="outline-none">
	{@render children()}
</main>
<Footer />
