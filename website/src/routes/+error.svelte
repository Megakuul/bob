<script lang="ts">
	import { page } from '$app/state';
	import Backdrop from '$lib/ui/Backdrop.svelte';
</script>

<svelte:head>
	<title>{page.status === 404 ? 'Not found' : 'Error'} · bob</title>
</svelte:head>

<section class="relative overflow-hidden border-b border-line">
	<Backdrop size={32} />
	<div class="relative mx-auto flex max-w-[1240px] flex-col gap-6 px-4 py-28 sm:px-6">
		<p class="font-mono text-sm text-muted"><span class="bg-accent px-1.5 text-on-accent">BOB-E-{page.status}</span> / {page.status === 404 ? 'label not found' : 'error'}</p>
		<h1 class="text-5xl leading-none font-extrabold tracking-tight lg:text-6xl">{page.status === 404 ? 'Nothing at this path.' : 'Something broke.'}</h1>
		<p class="max-w-[52ch] text-lg text-muted">
			{#if page.status === 404}
				<code class="font-mono text-fg">{page.url.pathname}</code> is not part of the graph. It may have moved when the docs were reorganised.
			{:else}
				{page.error?.message}
			{/if}
		</p>
		<div class="flex flex-wrap gap-3">
			<a href="/" class="rounded-xs border border-accent bg-accent px-5 py-2.5 font-mono text-base font-bold text-on-accent hover:border-fg">home</a>
			<a href="/docs" class="rounded-xs border border-line bg-bg px-5 py-2.5 font-mono text-base hover:border-fg">docs</a>
		</div>
	</div>
</section>
