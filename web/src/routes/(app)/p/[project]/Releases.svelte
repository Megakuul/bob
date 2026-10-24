<script lang="ts">
	import { client } from '$lib/api';
	import { ago, enumName } from '$lib/format';
	import { Paged } from '$lib/query.svelte';
	import { Visibility } from '$lib/gen/bob/common/v1/common_pb';
	import { ReleaseService, ReleaseState } from '$lib/gen/bob/release/v1/release_pb';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { project }: { project: string } = $props();

	const releases = client(ReleaseService);
	const list = new Paged((pageToken, signal) =>
		releases
			.listReleases({ projectId: project, pageToken }, { signal })
			.then((r) => ({ items: r.releases, next: r.nextPageToken }))
	);
</script>

{#if list.error}
	<EmptyState text={list.error} />
{:else}
	<Table count={list.items.length} empty="No releases.">
		{#snippet head()}
			<Th>Name</Th><Th>Version</Th><Th>Visibility</Th><Th>State</Th><Th>Platforms</Th><Th num>Created</Th>
		{/snippet}
		{#each list.items as r (r.id)}
			<Tr>
				<Td>{r.name}</Td>
				<Td><IdLink id={r.version} href="/p/{r.projectId}/releases/{r.name}/{r.version}" /></Td>
				<Td>{enumName(Visibility, r.visibility).toLowerCase()}</Td>
				<Td><StatusBadge state={enumName(ReleaseState, r.state)} /></Td>
				<Td mono>{r.closures.map((c) => c.platform).join(' ')}</Td>
				<Td num muted>{ago(r.createdAt)}</Td>
			</Tr>
		{/each}
	</Table>
	<Pagination next={list.next} prev={list.hasPrev} onnext={() => list.forward()} onprev={() => list.back()} />
{/if}
