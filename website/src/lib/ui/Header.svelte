<script lang="ts">
	import { page } from '$app/state';
	import { repo } from '$lib/site';
	import Logo from './Logo.svelte';
	import ThemeToggle from './ThemeToggle.svelte';

	const docs = $derived(page.url.pathname.startsWith('/docs'));
</script>

<header class="sticky top-0 z-30 border-b border-line bg-bg">
	<div class="mx-auto flex h-14 max-w-[1240px] items-center gap-6 px-4 sm:px-6">
		<Logo />
		<nav class="flex flex-1 items-center gap-5 font-mono text-sm" aria-label="Main">
			<a href="/docs" class="hover:text-fg {docs ? 'text-fg underline decoration-accent decoration-2 underline-offset-[6px]' : 'text-muted'}" aria-current={docs ? 'page' : undefined}>docs</a>
			<a href="/#pain" class="hidden text-muted hover:text-fg sm:inline">why bob</a>
			<a href="/#install" class="hidden text-muted hover:text-fg sm:inline">install</a>
			<a href="/#status" class="hidden text-muted hover:text-fg md:inline">status</a>
		</nav>
		<a href={repo} class="font-mono text-sm text-muted hover:text-fg" rel="external noopener">github</a>
		<ThemeToggle />
	</div>
</header>
