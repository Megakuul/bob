<script lang="ts">
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { matches } from '$lib/format';
	import { Paged, Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { RoleService, type Role } from '$lib/gen/bob/access/v1/access_pb';
	import Button from '$lib/ui/Button.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import Input from '$lib/ui/Input.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { ready = $bindable(false) }: { ready?: boolean } = $props();

	const roles = client(RoleService);
	let version = $state(0);
	const list = new Paged((pageToken, signal) => {
		void version;
		return roles.listRoles({ pageToken }, { signal }).then((r) => ({ items: r.roles, next: r.nextPageToken }));
	});
	$effect(() => {
		ready = list.ready;
	});
	const q = $derived(page.url.searchParams.get('query') ?? '');
	const rows = $derived(list.items.filter((r) => matches(q, r.id, r.description)));

	// Editor: permissions are method × project pairs, methods picked from the catalog.
	const catalog = new Query((signal) => roles.listPermissionCatalog({}, { signal }));
	const methods = $derived(catalog.data?.entries ?? []);
	let editing = $state(false);
	let existing = $state<Role>();
	let draft = $state({ id: '', description: '', permissions: [] as { method: string; project: string }[] });

	function edit(r?: Role) {
		existing = r;
		draft = {
			id: r?.id ?? '',
			description: r?.description ?? '',
			permissions: r?.permissions.map((p) => ({ method: p.method, project: p.project })) ?? [{ method: '', project: '*' }]
		};
		editing = true;
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		const role = { id: draft.id, description: draft.description, permissions: draft.permissions.filter((p) => p.method) };
		try {
			if (existing) {
				await roles.updateRole({ role: { ...role, etag: existing.etag }, updateMask: { paths: ['description', 'permissions'] } });
			} else {
				await roles.createRole({ role });
			}
			editing = false;
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

{#if list.error}
	<EmptyState text={list.error} />
{:else}
	<div class="flex flex-wrap items-center justify-between gap-2">
		<FilterBar />
		<Button variant="primary" onclick={() => edit()}>Create role</Button>
	</div>
	<Table count={rows.length} empty="No roles.">
		{#snippet head()}
			<Th>ID</Th><Th>Description</Th><Th num>Permissions</Th><Th></Th>
		{/snippet}
		{#each rows as r (r.id)}
			<Tr>
				<Td mono>{r.id}</Td>
				<Td>{r.description}</Td>
				<Td num>{r.builtIn ? 'all' : r.permissions.length}</Td>
				<Td>
					{#if !r.builtIn}
						<button type="button" class="text-sm text-muted hover:text-fg hover:underline" onclick={() => edit(r)}>edit</button>
					{/if}
				</Td>
			</Tr>
		{/each}
	</Table>
	<Pagination next={list.next} prev={list.hasPrev} onnext={() => list.forward()} onprev={() => list.back()} />
{/if}

<Dialog bind:open={editing} title={existing ? `Edit role ${existing.id}` : 'Create role'}>
	<form class="flex flex-col gap-3" onsubmit={save}>
		{#if !existing}<Input label="ID" bind:value={draft.id} required pattern={'[a-z][a-z0-9-]{1,38}[a-z0-9]'} />{/if}
		<Input label="Description" bind:value={draft.description} />
		<div class="flex flex-col gap-1">
			<span class="text-sm text-muted">Permissions (method × project)</span>
			{#each draft.permissions as p, i (i)}
				<div class="flex gap-2">
					<select bind:value={p.method} aria-label="method" class="h-7 min-w-0 flex-1 rounded-xs border border-line bg-bg px-1 font-mono text-sm text-fg">
						<option value="">choose a method</option>
						<option value="*">* (everything)</option>
						{#each methods as m (m.method)}<option value={m.method} title={m.description}>{m.method}</option>{/each}
					</select>
					<input bind:value={p.project} aria-label="project" class="h-7 w-24 rounded-xs border border-line bg-bg px-2 text-base text-fg" />
					<button type="button" class="text-sm text-muted hover:text-fail" onclick={() => draft.permissions.splice(i, 1)}>remove</button>
				</div>
			{/each}
			<div><button type="button" class="text-sm text-muted hover:text-fg hover:underline" onclick={() => draft.permissions.push({ method: '', project: '*' })}>add permission</button></div>
		</div>
		<div class="flex justify-end gap-2">
			<Button onclick={() => (editing = false)}>Cancel</Button>
			<Button variant="primary" type="submit">Save</Button>
		</div>
	</form>
</Dialog>
