<script lang="ts">
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { ago, date, enumName, matches } from '$lib/format';
	import { Paged, Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { ServiceAccountService, TokenScope } from '$lib/gen/bob/access/v1/access_pb';
	import Button from '$lib/ui/Button.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import CodeBlock from '$lib/ui/CodeBlock.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import Input from '$lib/ui/Input.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import Section from '$lib/ui/Section.svelte';
	import Select from '$lib/ui/Select.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { ready = $bindable(false) }: { ready?: boolean } = $props();

	const sas = client(ServiceAccountService);
	let version = $state(0);
	const list = new Paged((pageToken, signal) => {
		void version;
		return sas.listServiceAccounts({ pageToken }, { signal }).then((r) => ({ items: r.serviceAccounts, next: r.nextPageToken }));
	});
	$effect(() => {
		ready = list.ready;
	});
	const q = $derived(page.url.searchParams.get('query') ?? '');
	const rows = $derived(list.items.filter((s) => matches(q, s.id, s.description)));

	let selected = $state('');
	let tokVersion = $state(0);
	const tokens = new Query((signal) => {
		void tokVersion;
		return selected ? sas.listServiceAccountTokens({ serviceAccountId: selected }, { signal }) : Promise.resolve(undefined);
	});

	let creatingSa = $state(false);
	let sa = $state({ id: '', description: '' });
	async function createSa(e: SubmitEvent) {
		e.preventDefault();
		try {
			await sas.createServiceAccount({ serviceAccount: { ...sa } });
			creatingSa = false;
			selected = sa.id;
			sa = { id: '', description: '' };
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	// The secret is shown once, in the dialog that created it.
	let creatingTok = $state(false);
	let tok = $state({ name: '', scope: 'download', ttl: '90', noExpiry: false });
	let secret = $state('');
	async function createToken(e: SubmitEvent) {
		e.preventDefault();
		try {
			const r = await sas.createServiceAccountToken({
				serviceAccountId: selected,
				name: tok.name,
				scope: tok.scope === 'api' ? TokenScope.API : TokenScope.DOWNLOAD,
				ttlDays: tok.noExpiry ? 0 : Number(tok.ttl),
				noExpiry: tok.noExpiry
			});
			secret = r.secret;
			tokVersion++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function revoke(tokenId: string) {
		try {
			await sas.revokeServiceAccountToken({ serviceAccountId: selected, tokenId });
			tokVersion++;
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
		<Button variant="primary" onclick={() => (creatingSa = true)}>Create service account</Button>
	</div>
	<Table count={rows.length} empty="No service accounts.">
		{#snippet head()}
			<Th>ID</Th><Th>Description</Th><Th>Disabled</Th><Th num>Created</Th>
		{/snippet}
		{#each rows as s (s.id)}
			<Tr selected={selected === s.id} onclick={() => (selected = s.id)}>
				<Td mono>{s.id}</Td>
				<Td>{s.description}</Td>
				<Td>{s.disabled ? 'yes' : ''}</Td>
				<Td num muted>{ago(s.createdAt)}</Td>
			</Tr>
		{/each}
	</Table>
	<Pagination next={list.next} prev={list.hasPrev} onnext={() => list.forward()} onprev={() => list.back()} />
	{#if selected}
		<Section title="Tokens of {selected}">
			{#snippet actions()}
				<Button onclick={() => ((secret = ''), (creatingTok = true))}>Create token</Button>
			{/snippet}
			{#if tokens.error}
				<EmptyState text={tokens.error} />
			{:else}
				<Table count={tokens.data?.tokens.length ?? 0} empty="No tokens.">
					{#snippet head()}
						<Th>Name</Th><Th>Scope</Th><Th>Expires</Th><Th num>Last used</Th><Th num>Created</Th><Th></Th>
					{/snippet}
					{#each tokens.data?.tokens ?? [] as t (t.id)}
						<Tr>
							<Td>{t.name}</Td>
							<Td>{enumName(TokenScope, t.scope).toLowerCase()}</Td>
							<Td muted>{date(t.expiresAt)?.toISOString().slice(0, 10) ?? 'never'}</Td>
							<Td num muted>{ago(t.lastUsedOn)}</Td>
							<Td num muted>{ago(t.createdAt)}</Td>
							<Td><button type="button" class="text-sm text-muted hover:text-fail hover:underline" onclick={() => revoke(t.id)}>revoke</button></Td>
						</Tr>
					{/each}
				</Table>
			{/if}
		</Section>
	{/if}
{/if}

<Dialog bind:open={creatingSa} title="Create service account">
	<form class="flex flex-col gap-3" onsubmit={createSa}>
		<Input label="ID" bind:value={sa.id} required pattern={'[a-z][a-z0-9-]{1,38}[a-z0-9]'} />
		<Input label="Description" bind:value={sa.description} />
		<div class="flex justify-end gap-2">
			<Button onclick={() => (creatingSa = false)}>Cancel</Button>
			<Button variant="primary" type="submit">Create</Button>
		</div>
	</form>
</Dialog>

<Dialog bind:open={creatingTok} title="Create token for {selected}">
	{#if secret}
		<p>Copy the token now. It is not shown again.</p>
		<CodeBlock text={secret} />
		<div class="flex justify-end"><Button onclick={() => ((creatingTok = false), (secret = ''))}>Done</Button></div>
	{:else}
		<form class="flex flex-col gap-3" onsubmit={createToken}>
			<Input label="Name" bind:value={tok.name} required />
			<Select
				label="Scope"
				bind:value={tok.scope}
				options={[
					{ value: 'download', label: 'download (cache, images, releases)' },
					{ value: 'api', label: 'api' }
				]}
			/>
			<Input label="Valid for (days)" type="number" min="1" max="365" bind:value={tok.ttl} disabled={tok.noExpiry} />
			<Checkbox label="No expiry (admin only)" bind:checked={tok.noExpiry} />
			<div class="flex justify-end gap-2">
				<Button onclick={() => (creatingTok = false)}>Cancel</Button>
				<Button variant="primary" type="submit">Create</Button>
			</div>
		</form>
	{/if}
</Dialog>
