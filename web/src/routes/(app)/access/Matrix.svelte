<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { matches } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { RoleService } from '$lib/gen/bob/access/v1/access_pb';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { ready = $bindable(false) }: { ready?: boolean } = $props();

	const roles = client(RoleService);
	const bindings = new Query((signal) => roles.listRoleBindings({ pageSize: 500 }, { signal }));
	$effect(() => {
		ready = bindings.ready;
	});

	const params = $derived(page.url.searchParams);
	const all = $derived(bindings.data?.bindings ?? []);
	const kept = $derived(
		all.filter(
			(b) =>
				matches(params.get('query') ?? '', b.principal?.id, b.principal?.displayName) &&
				(!params.get('project') || b.project === params.get('project')) &&
				(!params.get('role') || b.role === params.get('role'))
		)
	);
	const projects = $derived([...new Set(kept.map((b) => b.project))].sort());
	const principals = $derived([...new Set(kept.map((b) => b.principal?.id ?? ''))].sort());
	const cell = (p: string, proj: string) =>
		kept
			.filter((b) => b.principal?.id === p && b.project === proj)
			.map((b) => b.role)
			.join(', ');
	const name = (p: string) => all.find((b) => b.principal?.id === p)?.principal?.displayName || p;
	const opts = (xs: string[]) => [...new Set(xs)].sort().map((x) => ({ value: x, label: x }));
</script>

{#if bindings.error}
	<EmptyState text={bindings.error} />
{:else}
	<FilterBar
		filters={[
			{ name: 'project', label: 'project', options: opts(all.map((b) => b.project)) },
			{ name: 'role', label: 'role', options: opts(all.map((b) => b.role)) }
		]}
	/>
	<Table count={principals.length} empty="No role bindings.">
		{#snippet head()}
			<Th>Principal</Th>
			{#each projects as proj (proj)}<Th>{proj === '*' ? 'all projects' : proj}</Th>{/each}
		{/snippet}
		{#each principals as p (p)}
			<Tr>
				<Td mono>{name(p)}</Td>
				{#each projects as proj (proj)}<Td>{cell(p, proj)}</Td>{/each}
			</Tr>
		{/each}
	</Table>
{/if}
