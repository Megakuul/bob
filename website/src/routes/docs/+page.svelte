<script lang="ts">
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>Docs · bob</title>
	<meta name="description" content="bob documentation: getting started, concepts, ecosystems, outputs and the server." />
</svelte:head>

<div class="flex flex-col gap-10">
	<header class="flex flex-col gap-4 border-b border-line pb-8">
		<p class="font-mono text-sm text-muted"><span class="bg-accent px-1.5 text-on-accent">docs</span> / index</p>
		<h1 class="text-4xl leading-tight font-extrabold tracking-tight md:text-5xl">Documentation</h1>
		<p class="max-w-[60ch] text-lg text-muted md:text-xl">
			How to describe a build in <code class="font-mono text-fg">bob.go</code>, run it locally and on the server, and ship what it produces.
			Press <kbd class="rounded-xs border border-line bg-surface px-1.5 font-mono text-sm text-fg">/</kbd> to search.
		</p>
	</header>
	{#if data.sections.length === 0}
		<p class="text-muted">No pages yet.</p>
	{/if}
	<div class="grid gap-px border border-line bg-line md:grid-cols-2">
		{#each data.sections as s, i (s.slug)}
			<section class="flex flex-col gap-4 bg-bg p-6" aria-labelledby="sec-{s.slug}">
				<p class="font-mono text-sm text-muted">{String(i + 1).padStart(2, '0')}</p>
				<h2 id="sec-{s.slug}" class="text-2xl font-bold tracking-tight">{s.title}</h2>
				<ul class="flex flex-col">
					{#each s.pages as p (p.path)}
						<li class="border-t border-line">
							<a href={p.path} class="group flex flex-col gap-0.5 py-3 hover:bg-surface">
								<span class="flex items-center justify-between font-semibold"
									>{p.title}<span class="font-mono text-muted group-hover:text-fg" aria-hidden="true">→</span></span
								>
								{#if p.summary}<span class="text-base text-muted">{p.summary}</span>{/if}
							</a>
						</li>
					{/each}
				</ul>
			</section>
		{/each}
	</div>
</div>
