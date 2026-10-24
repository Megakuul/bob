<script lang="ts">
	import { client } from '$lib/api';
	import { enumName } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { Concurrency, ProjectService } from '$lib/gen/bob/project/v1/project_pb';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { project }: { project: string } = $props();

	const projects = client(ProjectService);
	const list = new Query((signal) => projects.listPipelines({ projectId: project }, { signal }));
	const rows = $derived(list.data?.pipelines ?? []);
</script>

{#if list.error}
	<EmptyState text={list.error} />
{:else if list.data}
	<p class="text-muted">
		Evaluated at <span class="font-mono text-sm">{list.data.evaluatedSha.slice(0, 7) || '—'}</span>
	</p>
	<Table count={rows.length} empty="No pipelines in bob.go.">
		{#snippet head()}
			<Th>Name</Th><Th>Triggers</Th><Th>Concurrency</Th><Th>Releases</Th><Th>Defined at</Th><Th>Last build</Th><Th>State</Th>
		{/snippet}
		{#each rows as p (p.name)}
			<Tr>
				<Td>{p.name}</Td>
				<Td>{p.triggers.join(', ') || 'manual'}</Td>
				<Td>{enumName(Concurrency, p.concurrency).toLowerCase()}</Td>
				<Td>{p.releases ? 'yes' : ''}</Td>
				<Td mono>{p.definedAt?.definedAt}</Td>
				<Td>
					{#if p.lastBuildId}<IdLink id={p.lastBuildId} href="/p/{project}/builds/{p.lastBuildId}" />{/if}
				</Td>
				<Td>{#if p.lastState}<StatusBadge state={p.lastState.replace('BUILD_STATE_', '')} />{/if}</Td>
			</Tr>
		{/each}
	</Table>
{/if}
