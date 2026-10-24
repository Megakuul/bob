<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { buildHref, pipeline, ref, sha, stateName, trigger } from '$lib/build';
	import { ago, enumName, matches, span } from '$lib/format';
	import { Paged } from '$lib/query.svelte';
	import { BuildService, BuildState, TriggerKind, type Build } from '$lib/gen/bob/build/v1/build_pb';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { project }: { project: string } = $props();

	const builds = client(BuildService);
	const list = new Paged(
		(pageToken, signal) =>
			builds
				.listBuilds({ projectId: project, pageToken }, { signal })
				.then((r) => ({ items: r.builds, next: r.nextPageToken })),
		{ refresh: true }
	);

	const names = (e: Record<number, string>) =>
		Object.values(e)
			.filter((v): v is string => typeof v === 'string' && v !== 'UNSPECIFIED')
			.map((v) => ({ value: v, label: v.toLowerCase().replace('_', ' ') }));

	const filters = $derived([
		{ name: 'state', label: 'state', options: names(BuildState) },
		{
			name: 'pipeline',
			label: 'pipeline',
			options: [...new Set(list.items.map(pipeline).filter(Boolean))].map((p) => ({ value: p, label: p }))
		},
		{ name: 'trigger', label: 'trigger', options: names(TriggerKind) }
	]);

	const params = $derived(page.url.searchParams);
	const keep = (b: Build) =>
		matches(params.get('query') ?? '', b.id, String(b.number), pipeline(b), ref(b), sha(b)) &&
		(!params.get('state') || stateName(b) === params.get('state')) &&
		(!params.get('pipeline') || pipeline(b) === params.get('pipeline')) &&
		(!params.get('trigger') || enumName(TriggerKind, b.trigger?.kind ?? 0) === params.get('trigger'));
	const rows = $derived(list.items.filter(keep));
</script>

<FilterBar {filters} />
{#if list.error}
	<EmptyState text={list.error} />
{:else}
	<Table count={rows.length} empty="No builds.">
		{#snippet head()}
			<Th num>#</Th><Th>ID</Th><Th>Pipeline</Th><Th>Trigger</Th><Th>Ref/SHA</Th><Th>State</Th><Th num>Dur</Th><Th num>Created</Th><Th></Th>
		{/snippet}
		{#each rows as b (b.id)}
			<Tr>
				<Td num>{b.number}</Td>
				<Td><IdLink id={b.id} href={buildHref(b)} /></Td>
				<Td>{pipeline(b)}</Td>
				<Td>{trigger(b)}</Td>
				<Td mono>{ref(b)} <span class="text-muted">{sha(b)}</span></Td>
				<Td><StatusBadge state={stateName(b)} /></Td>
				<Td num>{span(b.startedAt, b.finishedAt)}</Td>
				<Td num muted>{ago(b.createdAt)}</Td>
				<Td muted>
					{#if b.trigger?.supersededBy}
						superseded by <a class="font-mono text-sm hover:underline" href="/p/{project}/builds/{b.trigger.supersededBy}">{b.trigger.supersededBy.slice(-7)}</a>
					{:else if b.trigger?.retryOf}
						retry of <a class="font-mono text-sm hover:underline" href="/p/{project}/builds/{b.trigger.retryOf}">{b.trigger.retryOf.slice(-7)}</a>
					{/if}
				</Td>
			</Tr>
		{/each}
	</Table>
	<Pagination next={list.next} prev={list.hasPrev} onnext={() => list.forward()} onprev={() => list.back()} />
{/if}
