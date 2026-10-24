<script lang="ts">
	import { copyText, shellCommands } from '$lib/copy';
	import Toc from '$lib/ui/Toc.svelte';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
	const doc = $derived(data.doc);

	// Copy buttons in the prerendered markdown: one delegated handler for the article.
	function click(e: MouseEvent) {
		const button = (e.target as HTMLElement).closest<HTMLElement>('[data-copy]');
		const code = button?.closest('.code')?.querySelector('pre');
		if (!button || !code) return;
		const text = code.textContent ?? '';
		copyText(code.querySelector('[data-lang=bash]') ? shellCommands(text) : text, button);
	}
</script>

<svelte:head>
	<title>{doc.title} · bob docs</title>
	{#if doc.summary}<meta name="description" content={doc.summary} />{/if}
</svelte:head>

<div class="grid gap-12 lg:grid-cols-[minmax(0,1fr)_200px]">
	<article class="min-w-0 max-w-[76ch]">
		<header class="mb-8 flex flex-col gap-3 border-b border-line pb-6">
			<p class="font-mono text-sm text-muted"><span class="bg-accent px-1.5 text-on-accent">docs</span> / {doc.sectionTitle}</p>
			<h1 class="text-3xl leading-tight font-extrabold tracking-tight md:text-4xl">{doc.title}</h1>
			{#if doc.summary}<p class="text-lg text-muted md:text-xl">{doc.summary}</p>{/if}
		</header>
		<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (the buttons inside handle keys) -->
		<div class="md" onclick={click}>
			{@html doc.html}
		</div>
		<nav class="mt-16 grid gap-px border border-line bg-line sm:grid-cols-2" aria-label="Previous and next page">
			{#if data.prev}
				<a href={data.prev.path} class="group flex flex-col gap-1 bg-bg p-4 hover:bg-surface">
					<span class="font-mono text-xs text-muted">← previous</span>
					<span class="font-semibold">{data.prev.title}</span>
				</a>
			{:else}
				<span class="hidden bg-bg sm:block"></span>
			{/if}
			{#if data.next}
				<a href={data.next.path} class="group flex flex-col gap-1 bg-bg p-4 text-right hover:bg-surface">
					<span class="font-mono text-xs text-muted">next →</span>
					<span class="font-semibold">{data.next.title}</span>
				</a>
			{/if}
		</nav>
	</article>
	<aside class="hidden lg:block">
		<div class="sticky top-24">
			<Toc headings={doc.headings} />
		</div>
	</aside>
</div>
