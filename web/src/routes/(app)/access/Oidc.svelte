<script lang="ts">
	import { client, errorText } from '$lib/api';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { OidcService } from '$lib/gen/bob/access/v1/access_pb';
	import Button from '$lib/ui/Button.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Input from '$lib/ui/Input.svelte';
	import Section from '$lib/ui/Section.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	let { ready = $bindable(false) }: { ready?: boolean } = $props();

	const oidc = client(OidcService);
	const providers = new Query((signal) => oidc.listOidcProviders({}, { signal }));
	$effect(() => {
		ready = providers.ready;
	});

	let selected = $state('');
	let version = $state(0);
	const mappings = new Query((signal) => {
		void version;
		return selected ? oidc.listOidcMappings({ providerId: selected }, { signal }) : Promise.resolve(undefined);
	});

	let m = $state({ claim: 'groups', value: '', role: '', project: '*' });
	async function add(e: SubmitEvent) {
		e.preventDefault();
		try {
			await oidc.createOidcMapping({ mapping: { providerId: selected, ...m } });
			m = { claim: m.claim, value: '', role: '', project: '*' };
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function remove(id: string) {
		try {
			await oidc.deleteOidcMapping({ id });
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

{#if providers.error}
	<EmptyState text={providers.error} />
{:else}
	<Table count={providers.data?.providers.length ?? 0} empty="No OIDC providers.">
		{#snippet head()}
			<Th>ID</Th><Th>Name</Th><Th>Issuer</Th><Th>Client</Th><Th>Username claim</Th><Th>Mapping required</Th>
		{/snippet}
		{#each providers.data?.providers ?? [] as p (p.id)}
			<Tr selected={selected === p.id} onclick={() => (selected = p.id)}>
				<Td mono>{p.id}</Td>
				<Td>{p.displayName}</Td>
				<Td mono>{p.issuerUrl}</Td>
				<Td mono>{p.clientId}</Td>
				<Td mono>{p.usernameClaim}</Td>
				<Td>{p.requireMapping ? 'yes' : 'no'}</Td>
			</Tr>
		{/each}
	</Table>
	{#if selected}
		<Section title="Mappings of {selected}">
			{#if mappings.error}
				<EmptyState text={mappings.error} />
			{:else}
				<Table count={mappings.data?.mappings.length ?? 0} empty="No mappings: users of this provider get no role.">
					{#snippet head()}
						<Th>Mapping</Th><Th></Th>
					{/snippet}
					{#each mappings.data?.mappings ?? [] as x (x.id)}
						<Tr>
							<Td mono>{x.claim} = {x.value} → {x.role} @ {x.project}</Td>
							<Td num><button type="button" class="text-sm text-muted hover:text-fail hover:underline" onclick={() => remove(x.id)}>remove</button></Td>
						</Tr>
					{/each}
				</Table>
			{/if}
			<form class="flex flex-wrap items-end gap-2" onsubmit={add}>
				<Input label="Claim" bind:value={m.claim} required />
				<Input label="Value" bind:value={m.value} required />
				<Input label="Role" bind:value={m.role} required />
				<Input label="Project" bind:value={m.project} required />
				<Button type="submit">Add mapping</Button>
			</form>
		</Section>
	{/if}
{/if}
