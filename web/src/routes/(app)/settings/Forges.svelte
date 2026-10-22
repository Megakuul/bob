<script lang="ts">
	import { client, errorText } from '$lib/api';
	import { ago, enumName } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { ForgeKind, ForgeService, Health } from '$lib/gen/bob/forge/v1/forge_pb';
	import Button from '$lib/ui/Button.svelte';
	import CodeBlock from '$lib/ui/CodeBlock.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Mono from '$lib/ui/Mono.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const forges = client(ForgeService);
	const list = new Query((signal) => forges.listForgeIntegrations({}, { signal }), { refresh: true });
	let selected = $state('');
	const deliveries = new Query((signal) =>
		selected ? forges.listForgeDeliveries({ integrationId: selected }, { signal }) : Promise.resolve(undefined)
	);

	// a rotated secret is shown once
	let secret = $state('');
	async function rotate(id: string) {
		try {
			secret = (await forges.rotateWebhookSecret({ id })).webhookSecret;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

{#if list.error}
	<EmptyState text={list.error} />
{:else}
	<Table count={list.data?.integrations.length ?? 0} empty="No forge integrations.">
		{#snippet head()}
			<Th>ID</Th><Th>Kind</Th><Th>Base URL</Th><Th>Health</Th><Th>Webhook URL</Th><Th></Th>
		{/snippet}
		{#each list.data?.integrations ?? [] as f (f.id)}
			<Tr selected={selected === f.id} onclick={() => (selected = f.id)}>
				<Td mono>{f.id}</Td>
				<Td>{enumName(ForgeKind, f.kind).toLowerCase()}</Td>
				<Td mono>{f.baseUrl}</Td>
				<Td><span title={f.healthMessage}><StatusBadge state={enumName(Health, f.health)} /></span></Td>
				<Td><Mono text={f.webhookUrl} copy /></Td>
				<Td><button type="button" class="text-sm text-muted hover:text-fg hover:underline" onclick={() => rotate(f.id)}>rotate secret</button></Td>
			</Tr>
		{/each}
	</Table>
	{#if selected}
		<Section title="Recent deliveries to {selected}">
			<Table count={deliveries.data?.deliveries.length ?? 0} empty="No deliveries yet.">
				{#snippet head()}
					<Th>Delivery</Th><Th>Event</Th><Th>Result</Th><Th>Build</Th><Th num>Received</Th>
				{/snippet}
				{#each deliveries.data?.deliveries ?? [] as d (d.deliveryId)}
					<Tr>
						<Td mono>{d.deliveryId}</Td>
						<Td>{d.event}</Td>
						<Td>{d.result}</Td>
						<Td mono>{d.buildId}</Td>
						<Td num muted>{ago(d.receivedAt)}</Td>
					</Tr>
				{/each}
			</Table>
		</Section>
	{/if}
{/if}

<Dialog open={!!secret} title="New webhook secret">
	<p>Enter it in the forge's webhook settings. The old secret is accepted for 24 hours. It is not shown again.</p>
	<CodeBlock text={secret} />
	<div class="flex justify-end"><Button onclick={() => (secret = '')}>Done</Button></div>
</Dialog>
