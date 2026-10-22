<script lang="ts">
	// Filter state lives in the URL query string (R-UI-006): the text filter is `query`, enum
	// filters use their own names. Pages filter the loaded rows from page.url.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';

	type Filter = { name: string; label: string; options: { value: string; label: string }[] };
	let { filters = [], placeholder = 'filter' }: { filters?: Filter[]; placeholder?: string } = $props();

	function set(name: string, value: string) {
		const u = new URL(page.url);
		if (value) u.searchParams.set(name, value);
		else u.searchParams.delete(name);
		goto(u, { replaceState: true, keepFocus: true, noScroll: true });
	}
</script>

<div class="flex flex-wrap items-center gap-3">
	<input
		name="query"
		type="search"
		aria-label="filter"
		{placeholder}
		value={page.url.searchParams.get('query') ?? ''}
		oninput={(e) => set('query', e.currentTarget.value)}
		class="h-7 w-56 rounded-xs border border-line bg-bg px-2 text-base text-fg placeholder:text-muted"
	/>
	{#each filters as f (f.name)}
		<label class="flex items-center gap-1 text-sm text-muted">
			{f.label}
			<select
				name={f.name}
				value={page.url.searchParams.get(f.name) ?? ''}
				onchange={(e) => set(f.name, e.currentTarget.value)}
				class="h-7 rounded-xs border border-line bg-bg px-1 text-base text-fg"
			>
				<option value="">any</option>
				{#each f.options as o (o.value)}
					<option value={o.value}>{o.label}</option>
				{/each}
			</select>
		</label>
	{/each}
</div>
