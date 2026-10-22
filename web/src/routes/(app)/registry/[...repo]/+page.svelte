<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { ago, bytes } from '$lib/format';
	import { Paged, Query } from '$lib/query.svelte';
	import { RegistryService } from '$lib/gen/bob/registry/v1/registry_pb';
	import CodeBlock from '$lib/ui/CodeBlock.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import Section from '$lib/ui/Section.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const registry = client(RegistryService);
	const name = $derived(page.params.repo ?? '');
	const repo = new Query((signal) => registry.getRepository({ name }, { signal }));
	const tags = new Paged((pageToken, signal) =>
		registry.listTags({ repository: name, pageToken }, { signal }).then((r) => ({ items: r.tags, next: r.nextPageToken }))
	);
	const pull = $derived(repo.data?.repository?.pullRef || name);
</script>

<svelte:head><title>{name} · bob</title></svelte:head>

<div data-page={repo.ready && tags.ready ? 'repository' : undefined} class="flex flex-col gap-5">
	<PageHeader title={name} crumbs={[{ label: 'Registry', href: '/registry?tab=images' }]}>
		{#snippet meta()}
			{#if repo.data?.repository}
				{@const r = repo.data.repository}
				<a href="/p/{r.projectId}" class="hover:underline">{r.projectId}</a>
				<span>{r.tagCount} tags</span>
				<span>{r.hasPublicTags ? 'public tags' : 'private'}</span>
			{/if}
		{/snippet}
	</PageHeader>
	{#if repo.error}
		<EmptyState text={repo.error} />
	{:else}
		<Section title="Pull">
			<CodeBlock text="podman pull {pull}:{tags.items[0]?.tag ?? 'latest'}" />
		</Section>
		<Section title="Tags">
			{#if tags.error}
				<EmptyState text={tags.error} />
			{:else}
				<Table count={tags.items.length} empty="No visible tags.">
					{#snippet head()}
						<Th>Tag</Th><Th>Digest</Th><Th>Platforms</Th><Th num>Size</Th><Th>Public</Th><Th>Release</Th><Th num>Created</Th>
					{/snippet}
					{#each tags.items as t (t.tag)}
						<Tr>
							<Td mono>{t.tag}</Td>
							<Td mono muted><span title={t.digest}>{t.digest.slice(0, 19)}</span></Td>
							<Td mono>{t.platforms.join(' ')}</Td>
							<Td num>{bytes(t.size)}</Td>
							<Td>{t.public ? 'yes' : 'no'}</Td>
							<Td mono>{t.releaseId}</Td>
							<Td num muted>{ago(t.createdAt)}</Td>
						</Tr>
					{/each}
				</Table>
				<Pagination next={tags.next} prev={tags.hasPrev} onnext={() => tags.forward()} onprev={() => tags.back()} />
			{/if}
		</Section>
	{/if}
</div>
