<script lang="ts">
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import Search from '$lib/ui/Search.svelte';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps & { children: Snippet } = $props();

	let menu = $state(false);
	const path = $derived(page.url.pathname);
	const current = $derived(data.sections.flatMap((s) => s.pages).find((p) => p.path === path));

	// Close the phone menu after navigating.
	$effect(() => {
		void path;
		menu = false;
	});
</script>

{#snippet nav()}
	<nav aria-label="Docs" class="flex flex-col gap-6">
		<a href="/docs" class="font-mono text-sm {path === '/docs' ? 'text-fg' : 'text-muted hover:text-fg'}" aria-current={path === '/docs' ? 'page' : undefined}>overview</a>
		{#each data.sections as s (s.slug)}
			<div class="flex flex-col gap-1.5">
				<p class="font-mono text-xs tracking-wider text-muted uppercase">{s.title}</p>
				<ul class="flex flex-col border-l border-line">
					{#each s.pages as p (p.path)}
						<li>
							<a
								href={p.path}
								aria-current={p.path === path ? 'page' : undefined}
								class="-ml-px block border-l-2 py-1 pl-3 text-base {p.path === path ? 'border-accent font-semibold text-fg' : 'border-transparent text-muted hover:border-line hover:text-fg'}"
								>{p.title}</a
							>
						</li>
					{/each}
				</ul>
			</div>
		{/each}
	</nav>
{/snippet}

<div class="mx-auto grid max-w-[1240px] gap-x-12 px-4 sm:px-6 lg:grid-cols-[220px_1fr]">
	<aside class="lg:sticky lg:top-14 lg:h-[calc(100dvh-56px)] lg:overflow-y-auto lg:py-10">
		<div class="flex items-center gap-2 border-b border-line py-3 lg:border-0 lg:py-0">
			<button
				type="button"
				class="rounded-xs border border-line px-2.5 py-1.5 font-mono text-sm lg:hidden"
				aria-expanded={menu}
				aria-controls="docs-nav"
				onclick={() => (menu = !menu)}>{menu ? 'close' : 'menu'}</button
			>
			<div class="flex-1"><Search index={data.search} /></div>
		</div>
		{#if current && !menu}
			<p class="py-2 font-mono text-sm text-muted lg:hidden">{current.sectionTitle} / <span class="text-fg">{current.title}</span></p>
		{/if}
		<div id="docs-nav" class="{menu ? 'block' : 'hidden'} border-b border-line py-6 lg:mt-8 lg:block lg:border-0 lg:py-0">
			{@render nav()}
		</div>
	</aside>
	<div class="min-w-0 py-10">
		{@render children()}
	</div>
</div>
