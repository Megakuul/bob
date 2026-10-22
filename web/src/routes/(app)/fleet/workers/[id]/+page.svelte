<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { draining, labels, revoked, workerAction } from '$lib/fleet';
	import { ago, bytes, enumName } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { FleetService, WorkerState } from '$lib/gen/bob/fleet/v1/fleet_pb';
	import Button from '$lib/ui/Button.svelte';
	import Confirm from '$lib/ui/Confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import KeyValue from '$lib/ui/KeyValue.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';

	const fleet = client(FleetService);
	const id = $derived(page.params.id ?? '');
	let version = $state(0);
	const worker = new Query(
		(signal) => {
			void version;
			return fleet.getWorker({ id }, { signal });
		},
		{ refresh: true }
	);
	const w = $derived(worker.data?.worker);
	let confirming = $state(false);

	async function act(kind: 'drain' | 'undrain' | 'revoke') {
		if (await workerAction(kind, id)) version++;
	}
</script>

<svelte:head><title>Worker {id} · bob</title></svelte:head>

<div data-page={worker.ready ? 'worker' : undefined} class="flex flex-col gap-5">
	<PageHeader title={id} crumbs={[{ label: 'Fleet', href: '/fleet' }]}>
		{#snippet meta()}
			{#if w}<StatusBadge state={enumName(WorkerState, w.state)} /><span class="font-mono text-sm">{w.platform}</span>{/if}
		{/snippet}
		{#snippet actions()}
			{#if w && !revoked(w)}
				{#if draining(w)}
					<Button onclick={() => act('undrain')}>Undrain</Button>
				{:else}
					<Button onclick={() => act('drain')}>Drain</Button>
				{/if}
				<Button variant="danger" onclick={() => (confirming = true)}>Revoke</Button>
			{/if}
		{/snippet}
	</PageHeader>
	{#if worker.error}
		<EmptyState text={worker.error} />
	{:else if w}
		<Section title="Worker">
			<KeyValue
				rows={[
					{ k: 'Platform', v: w.platform, mono: true },
					{ k: 'Version', v: w.version, mono: true },
					{ k: 'CPUs', v: String(w.cpus) },
					{ k: 'Memory', v: bytes(w.memoryBytes) },
					{ k: 'Labels', v: labels(w), mono: true },
					{ k: 'Capabilities', v: w.capabilities.join(', ') },
					{ k: 'Registered', v: ago(w.registeredAt) + ' ago' },
					{ k: 'Last heartbeat', v: ago(w.lastHeartbeat) + ' ago' }
				]}
			/>
		</Section>
		<Section title="Running jobs">
			{#if w.jobIds.length}
				<ul class="flex flex-col gap-1">
					{#each w.jobIds as j (j)}<li class="font-mono text-sm">{j}</li>{/each}
				</ul>
			{:else}
				<EmptyState text="Idle." />
			{/if}
		</Section>
	{/if}
</div>

<Confirm
	bind:open={confirming}
	title="Revoke worker {id}?"
	text="Its credentials stop working at once; running jobs are requeued. A revoked worker must register again."
	confirm="Revoke"
	danger
	onconfirm={() => act('revoke')}
/>
