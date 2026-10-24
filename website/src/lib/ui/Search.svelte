<script lang="ts">
	import { goto } from '$app/navigation';
	import { search, type Entry } from '$lib/search';

	let { index }: { index: Entry[] } = $props();

	let query = $state('');
	let active = $state(0);
	let input: HTMLInputElement | undefined = $state();
	const hits = $derived(search(index, query));
	const id = $props.id();

	function key(e: KeyboardEvent) {
		if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
			e.preventDefault();
			if (hits.length) active = (active + (e.key === 'ArrowDown' ? 1 : hits.length - 1)) % hits.length;
		} else if (e.key === 'Enter' && hits[active]) {
			e.preventDefault();
			open(hits[active].href);
		} else if (e.key === 'Escape') {
			query = '';
			input?.blur();
		}
	}

	function open(href: string) {
		query = '';
		goto(href);
	}

	// "/" focuses the search from anywhere on a docs page.
	function global(e: KeyboardEvent) {
		const t = e.target as HTMLElement | null;
		if (e.key === '/' && !e.metaKey && !e.ctrlKey && !(t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)))) {
			e.preventDefault();
			input?.focus();
		}
	}
</script>

<svelte:window onkeydown={global} />

<div class="relative">
	<label for="q-{id}" class="sr-only">Search the docs</label>
	<div class="flex items-center border border-line bg-bg focus-within:border-fg">
		<svg viewBox="0 0 16 16" width="14" height="14" class="ml-2.5 shrink-0 text-muted" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5"
			><circle cx="7" cy="7" r="4.5" /><path d="M10.5 10.5L14 14" /></svg
		>
		<input
			bind:this={input}
			bind:value={query}
			oninput={() => (active = 0)}
			onkeydown={key}
			id="q-{id}"
			type="search"
			autocomplete="off"
			spellcheck="false"
			placeholder="search docs"
			role="combobox"
			aria-expanded={hits.length > 0}
			aria-controls="r-{id}"
			aria-activedescendant={hits.length ? `r-${id}-${active}` : undefined}
			class="w-full min-w-0 bg-transparent px-2 py-1.5 font-mono text-sm outline-none placeholder:text-muted"
		/>
		<kbd class="mr-2 rounded-xs border border-line px-1 font-mono text-xs text-muted" aria-hidden="true">/</kbd>
	</div>
	{#if query.trim()}
		<ul id="r-{id}" role="listbox" class="absolute top-full right-0 left-0 z-40 mt-1 max-h-96 overflow-auto border border-line bg-bg lg:right-auto lg:w-[28rem]">
			{#each hits as h, i (h.href)}
				<li id="r-{id}-{i}" role="option" aria-selected={i === active}>
					<a
						href={h.href}
						onclick={(e) => {
							e.preventDefault();
							open(h.href);
						}}
						onmousemove={() => (active = i)}
						class="flex flex-col border-l-2 px-3 py-2 {i === active ? 'border-accent bg-surface' : 'border-transparent'}"
					>
						<span class="text-base font-semibold">{h.title}</span>
						<span class="truncate text-sm text-muted">{h.context}</span>
					</a>
				</li>
			{:else}
				<li class="px-3 py-2 text-sm text-muted">No matches.</li>
			{/each}
		</ul>
	{/if}
</div>
