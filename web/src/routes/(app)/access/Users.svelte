<script lang="ts">
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { ago, enumName, matches } from '$lib/format';
	import { Paged, Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { RoleService, UserService, UserSource, type User } from '$lib/gen/bob/access/v1/access_pb';
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

	const users = client(UserService);
	const roles = client(RoleService);
	let version = $state(0);
	const list = new Paged(
		(pageToken, signal) => {
			void version;
			return users
				.listUsers({ pageToken, includeDisabled: true }, { signal })
				.then((r) => ({ items: r.users, next: r.nextPageToken }));
		},
		{ refresh: true }
	);
	const bindings = new Query((signal) => roles.listRoleBindings({ pageSize: 500 }, { signal }));
	$effect(() => {
		ready = list.ready && bindings.ready;
	});

	// roles per principal, "admin @ *, dev @ api"
	const roleText = $derived.by(() => {
		const m = new Map<string, string[]>();
		for (const b of bindings.data?.bindings ?? []) {
			const id = b.principal?.id ?? '';
			m.set(id, [...(m.get(id) ?? []), `${b.role} @ ${b.project}`]);
		}
		return m;
	});
	const q = $derived(page.url.searchParams.get('query') ?? '');
	const source = $derived(page.url.searchParams.get('source') ?? '');
	const rows = $derived(
		list.items.filter((u) => matches(q, u.id, u.login, u.displayName, u.email) && (!source || enumName(UserSource, u.source) === source))
	);

	let creating = $state(false);
	let draft = $state({ login: '', displayName: '', email: '', password: '' });

	async function create(e: SubmitEvent) {
		e.preventDefault();
		try {
			await users.createUser({
				user: { login: draft.login, displayName: draft.displayName, email: draft.email, mustChangePassword: true },
				initialPassword: draft.password
			});
			creating = false;
			draft = { login: '', displayName: '', email: '', password: '' };
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function setDisabled(u: User, disabled: boolean) {
		try {
			await users.updateUser({ user: { ...u, disabled }, updateMask: { paths: ['disabled'] } });
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

<div class="flex flex-wrap items-center justify-between gap-2">
	<FilterBar
		filters={[
			{
				name: 'source',
				label: 'source',
				options: [
					{ value: 'LOCAL', label: 'local' },
					{ value: 'OIDC', label: 'oidc' }
				]
			}
		]}
	/>
	<Button variant="primary" onclick={() => (creating = true)}>Create user</Button>
</div>
{#if list.error}
	<EmptyState text={list.error} />
{:else}
	<Table count={rows.length} empty="No users.">
		{#snippet head()}
			<Th>Login</Th><Th>Name</Th><Th>Source</Th><Th>Roles</Th><Th num>Last login</Th><Th>Disabled</Th><Th></Th>
		{/snippet}
		{#each rows as u (u.id)}
			<Tr>
				<Td mono>{u.login}</Td>
				<Td>{u.displayName}</Td>
				<Td muted>{enumName(UserSource, u.source).toLowerCase()}</Td>
				<Td>{(roleText.get(`user:${u.id}`) ?? roleText.get(u.id) ?? []).join(', ')}</Td>
				<Td num muted>{ago(u.lastLoginAt)}</Td>
				<Td>{u.disabled ? 'yes' : ''}</Td>
				<Td>
					<button type="button" class="text-sm text-muted hover:text-fg hover:underline" onclick={() => setDisabled(u, !u.disabled)}>
						{u.disabled ? 'enable' : 'disable'}
					</button>
				</Td>
			</Tr>
		{/each}
	</Table>
	<Pagination next={list.next} prev={list.hasPrev} onnext={() => list.forward()} onprev={() => list.back()} />
{/if}

<Dialog bind:open={creating} title="Create local user">
	<form class="flex flex-col gap-3" onsubmit={create}>
		<Input label="Login" bind:value={draft.login} required autocomplete="off" />
		<Input label="Display name" bind:value={draft.displayName} />
		<Input label="Email" type="email" bind:value={draft.email} />
		<Input label="Initial password (12+ characters)" type="password" minlength={12} bind:value={draft.password} required autocomplete="new-password" />
		<p class="text-sm text-muted">The user must change it at first login.</p>
		<div class="flex justify-end gap-2">
			<Button onclick={() => (creating = false)}>Cancel</Button>
			<Button variant="primary" type="submit">Create</Button>
		</div>
	</form>
</Dialog>
