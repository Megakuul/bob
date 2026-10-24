<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { draining, labels, revoked, workerAction } from '$lib/fleet';
	import { ago, bytes, enumName, matches } from '$lib/format';
	import { Paged, Query } from '$lib/query.svelte';
	import { FleetService, WorkerState, type Worker } from '$lib/gen/bob/fleet/v1/fleet_pb';
	import Confirm from '$lib/ui/Confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Pagination from '$lib/ui/Pagination.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const fleet = client(FleetService);
	let version = $state(0);
	const workers = new Paged(
		(pageToken, signal) => {
			void version;
			return fleet.listWorkers({ pageToken }, { signal }).then((r) => ({ items: r.workers, next: r.nextPageToken }));
		},
		{ refresh: true }
	);
	const orchestrators = new Query((signal) => fleet.listOrchestrators({}, { signal }), { refresh: true });

	const params = $derived(page.url.searchParams);
	const q = $derived(params.get('query') ?? '');
	const wstate = (w: Worker) => enumName(WorkerState, w.state);
	const rows = $derived(
		workers.items.filter(
			(w) =>
				matches(q, w.id, w.platform, labels(w)) &&
				(!params.get('platform') || w.platform === params.get('platform')) &&
				(!params.get('state') || wstate(w) === params.get('state'))
		)
	);
	const orchs = $derived((orchestrators.data?.orchestrators ?? []).filter((o) => matches(q, o.id, o.endpoint)));
	const filters = $derived([
		{
			name: 'platform',
			label: 'platform',
			options: [...new Set(workers.items.map((w) => w.platform))].map((p) => ({ value: p, label: p }))
		},
		{
			name: 'state',
			label: 'state',
			options: ['ACTIVE', 'DRAINING', 'STALE', 'REVOKED'].map((s) => ({ value: s, label: s.toLowerCase() }))
		}
	]);

	let revoking = $state('');
	let confirming = $state(false);
	async function act(kind: 'drain' | 'undrain' | 'revoke', id: string) {
		if (await workerAction(kind, id)) version++;
	}
</script>

<svelte:head><title>Fleet · bob</title></svelte:head>

<div data-page={workers.ready && orchestrators.ready ? 'fleet' : undefined} class="flex flex-col gap-5">
	<PageHeader title="Fleet" />
	<FilterBar {filters} />
	<Section title="Workers">
		{#if workers.error}
			<EmptyState text={workers.error} />
		{:else}
			<Table count={rows.length} empty="No workers.">
				{#snippet head()}
					<Th>ID</Th><Th>Platform</Th><Th>State</Th><Th num>Jobs</Th><Th num>CPU</Th><Th num>Mem</Th><Th num>Heartbeat</Th><Th>Labels</Th><Th>Actions</Th>
				{/snippet}
				{#each rows as w (w.id)}
					<Tr>
						<Td><IdLink id={w.id} href="/fleet/workers/{w.id}" /></Td>
						<Td mono>{w.platform}</Td>
						<Td><StatusBadge state={wstate(w)} /></Td>
						<Td num>{w.jobIds.length}</Td>
						<Td num>{w.cpus}</Td>
						<Td num>{bytes(w.memoryBytes)}</Td>
						<Td num muted>{ago(w.lastHeartbeat)}</Td>
						<Td mono muted>{labels(w)}</Td>
						<Td>
							{#if !revoked(w)}
								<span class="flex gap-3 text-sm">
									{#if draining(w)}
										<button type="button" class="text-muted hover:text-fg hover:underline" onclick={() => act('undrain', w.id)}>undrain</button>
									{:else}
										<button type="button" class="text-muted hover:text-fg hover:underline" onclick={() => act('drain', w.id)}>drain</button>
									{/if}
									<button type="button" class="text-muted hover:text-fail hover:underline" onclick={() => ((revoking = w.id), (confirming = true))}>revoke</button>
								</span>
							{/if}
						</Td>
					</Tr>
				{/each}
			</Table>
			<Pagination next={workers.next} prev={workers.hasPrev} onnext={() => workers.forward()} onprev={() => workers.back()} />
		{/if}
	</Section>
	<Section title="Orchestrators">
		{#if orchestrators.error}
			<EmptyState text={orchestrators.error} />
		{:else}
			<Table count={orchs.length} empty="No orchestrators.">
				{#snippet head()}
					<Th>ID</Th><Th>Endpoint</Th><Th>Version</Th><Th num>Started</Th><Th num>Heartbeat</Th><Th>Leader of</Th>
				{/snippet}
				{#each orchs as o (o.id)}
					<Tr>
						<Td mono>{o.id}</Td>
						<Td mono>{o.endpoint}</Td>
						<Td mono>{o.version}</Td>
						<Td num muted>{ago(o.startedAt)}</Td>
						<Td num muted>{ago(o.lastHeartbeat)}</Td>
						<Td>{o.leaderOf.join(', ')}</Td>
					</Tr>
				{/each}
			</Table>
		{/if}
	</Section>
</div>

<Confirm
	bind:open={confirming}
	title="Revoke worker {revoking}?"
	text="Its credentials stop working at once; running jobs are requeued. A revoked worker must register again."
	confirm="Revoke"
	danger
	onconfirm={() => act('revoke', revoking)}
/>
