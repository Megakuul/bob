<script lang="ts">
	import { client, errorText } from '$lib/api';
	import { ago, enumName } from '$lib/format';
	import { Paged } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { BindingSource, RoleService } from '$lib/gen/bob/access/v1/access_pb';
	import Button from '$lib/ui/Button.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Input from '$lib/ui/Input.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { project }: { project: string } = $props();

	const roles = client(RoleService);
	let version = $state(0);
	const list = new Paged((pageToken, signal) => {
		void version; // re-read after a change
		return roles
			.listRoleBindings({ project, pageToken }, { signal })
			.then((r) => ({ items: r.bindings, next: r.nextPageToken }));
	});

	let principal = $state('');
	let role = $state('');

	async function add(e: SubmitEvent) {
		e.preventDefault();
		try {
			await roles.createRoleBinding({ principalId: principal, role, project });
			principal = role = '';
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function remove(id: string) {
		try {
			await roles.deleteRoleBinding({ id, project });
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

{#if list.error}
	<EmptyState text={list.error} />
{:else}
	<Table count={list.items.length} empty="No role bindings on this project.">
		{#snippet head()}
			<Th>Principal</Th><Th>Role</Th><Th>Source</Th><Th num>Created</Th><Th></Th>
		{/snippet}
		{#each list.items as b (b.id)}
			<Tr>
				<Td mono>{b.principal?.displayName || b.principal?.id}</Td>
				<Td>{b.role}</Td>
				<Td muted>{enumName(BindingSource, b.source).toLowerCase()}</Td>
				<Td num muted>{ago(b.createdAt)}</Td>
				<Td num>
					{#if b.source !== BindingSource.OIDC}
						<button type="button" class="text-sm text-muted hover:text-fail hover:underline" onclick={() => remove(b.id)}>remove</button>
					{/if}
				</Td>
			</Tr>
		{/each}
	</Table>
	<Pagination next={list.next} prev={list.hasPrev} onnext={() => list.forward()} onprev={() => list.back()} />
	<form class="flex flex-wrap items-end gap-2" onsubmit={add}>
		<Input label="Principal (user:… or sa:…)" bind:value={principal} required />
		<Input label="Role" bind:value={role} required />
		<Button type="submit">Add binding</Button>
	</form>
{/if}
