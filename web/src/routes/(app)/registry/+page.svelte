<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { ago, enumName, matches } from '$lib/format';
	import { Paged } from '$lib/query.svelte';
	import { Visibility } from '$lib/gen/bob/common/v1/common_pb';
	import { RegistryService } from '$lib/gen/bob/registry/v1/registry_pb';
	import { ReleaseService } from '$lib/gen/bob/release/v1/release_pb';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Tabs from '$lib/ui/Tabs.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const releases = client(ReleaseService);
	const registry = client(RegistryService);
	const tab = $derived(page.url.searchParams.get('tab') ?? 'releases');
	const q = $derived(page.url.searchParams.get('query') ?? '');

	// Both lists load up front so switching tabs is instant; each refreshes while visible.
	const rel = new Paged(
		(pageToken, signal) =>
			releases.listReleases({ pageToken }, { signal }).then((r) => ({ items: r.releases, next: r.nextPageToken })),
		{ refresh: true }
	);
	const repos = new Paged(
		(pageToken, signal) =>
			registry.listRepositories({ pageToken }, { signal }).then((r) => ({ items: r.repositories, next: r.nextPageToken })),
		{ refresh: true }
	);

	const relRows = $derived(rel.items.filter((r) => matches(q, r.projectId, r.name, r.version)));
	const repoRows = $derived(repos.items.filter((r) => matches(q, r.name, r.projectId)));
</script>

<svelte:head><title>Registry · bob</title></svelte:head>

<div data-page={rel.ready && repos.ready ? 'registry' : undefined} class="flex flex-col gap-4">
	<PageHeader title="Registry" />
	<Tabs
		tabs={[
			{ id: 'releases', label: 'Releases' },
			{ id: 'images', label: 'Images' }
		]}
	/>
	<FilterBar />
	{#if tab === 'images'}
		{#if repos.error}
			<EmptyState text={repos.error} />
		{:else}
			<Table count={repoRows.length} empty="No images.">
				{#snippet head()}
					<Th>Repository</Th><Th>Project</Th><Th num>Tags</Th><Th>Public</Th><Th num>Updated</Th>
				{/snippet}
				{#each repoRows as r (r.name)}
					<Tr>
						<Td><IdLink id={r.name} href="/registry/{r.name}" /></Td>
						<Td><a href="/p/{r.projectId}" class="hover:underline">{r.projectId}</a></Td>
						<Td num>{r.tagCount}</Td>
						<Td>{r.hasPublicTags ? 'yes' : 'no'}</Td>
						<Td num muted>{ago(r.updatedAt)}</Td>
					</Tr>
				{/each}
			</Table>
			<Pagination next={repos.next} prev={repos.hasPrev} onnext={() => repos.forward()} onprev={() => repos.back()} />
		{/if}
	{:else if rel.error}
		<EmptyState text={rel.error} />
	{:else}
		<Table count={relRows.length} empty="No releases.">
			{#snippet head()}
				<Th>Project</Th><Th>Name</Th><Th>Version</Th><Th>Visibility</Th><Th>Platforms</Th><Th num>Date</Th>
			{/snippet}
			{#each relRows as r (r.id)}
				<Tr>
					<Td><a href="/p/{r.projectId}" class="hover:underline">{r.projectId}</a></Td>
					<Td>{r.name}</Td>
					<Td><IdLink id={r.version} href="/p/{r.projectId}/releases/{r.name}/{r.version}" /></Td>
					<Td>{enumName(Visibility, r.visibility).toLowerCase()}</Td>
					<Td mono>{r.closures.map((c) => c.platform).join(' ')}</Td>
					<Td num muted>{ago(r.createdAt)}</Td>
				</Tr>
			{/each}
		</Table>
		<Pagination next={rel.next} prev={rel.hasPrev} onnext={() => rel.forward()} onprev={() => rel.back()} />
	{/if}
</div>
