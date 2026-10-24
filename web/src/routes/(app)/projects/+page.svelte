<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { ago, enumName, matches } from '$lib/format';
	import { Paged } from '$lib/query.svelte';
	import { LinkHealth, ProjectService } from '$lib/gen/bob/project/v1/project_pb';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const projects = client(ProjectService);
	const list = new Paged(
		(pageToken, signal) =>
			projects.listProjects({ pageToken }, { signal }).then((r) => ({ items: r.projects, next: r.nextPageToken })),
		{ refresh: true }
	);

	const q = $derived(page.url.searchParams.get('query') ?? '');
	const rows = $derived(list.items.filter((p) => matches(q, p.id, p.displayName, p.repoFullName)));
</script>

<svelte:head><title>Projects · bob</title></svelte:head>

<div data-page={list.ready ? 'projects' : undefined} class="flex flex-col gap-4">
	<PageHeader title="Projects" />
	<FilterBar />
	{#if list.error}
		<EmptyState text={list.error} />
	{:else}
		<Table count={rows.length} empty="No projects.">
			{#snippet head()}
				<Th>ID</Th><Th>Name</Th><Th>Repository</Th><Th>Default branch</Th><Th>Link</Th><Th num>Created</Th>
			{/snippet}
			{#each rows as p (p.id)}
				<Tr>
					<Td><IdLink id={p.id} href="/p/{p.id}" /></Td>
					<Td>{p.displayName}</Td>
					<Td mono>{p.repoFullName}</Td>
					<Td mono>{p.defaultBranch}</Td>
					<Td><StatusBadge state={enumName(LinkHealth, p.linkHealth)} /></Td>
					<Td num muted>{ago(p.createdAt)}</Td>
				</Tr>
			{/each}
		</Table>
		<Pagination next={list.next} prev={list.hasPrev} onnext={() => list.forward()} onprev={() => list.back()} />
	{/if}
</div>
