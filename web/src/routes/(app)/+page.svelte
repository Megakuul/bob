<script lang="ts">
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { buildHref, errorCode, pipeline, progress, ref, stateName } from '$lib/build';
	import { ago, matches, percent } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { AnalyticsService } from '$lib/gen/bob/analytics/v1/analytics_pb';
	import type { Build } from '$lib/gen/bob/build/v1/build_pb';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatStrip from '$lib/ui/StatStrip.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const analytics = client(AnalyticsService);
	const overview = new Query((signal) => analytics.getOverview({}, { signal }), { refresh: true });

	const q = $derived(page.url.searchParams.get('query') ?? '');
	const keep = (b: Build) => matches(q, b.id, b.projectId, pipeline(b), ref(b));
	const running = $derived((overview.data?.running ?? []).filter(keep));
	const failures = $derived((overview.data?.recentFailures ?? []).filter(keep));

	const cells = $derived.by(() => {
		const o = overview.data;
		if (!o) return [];
		const workers = o.fleet.map((f) => `${f.platform.replace('linux/', '')} ${f.busy}/${f.active}`).join('  ');
		return [
			{ label: 'Running', value: String(o.running.length) },
			{ label: 'Queue', value: String(o.queueDepth) },
			{ label: 'Workers busy/active', value: workers || '0' },
			{ label: 'Cache hit rate 24 h', value: percent(o.cacheHitRate24h) }
		];
	});
</script>

<svelte:head><title>Overview · bob</title></svelte:head>

<div data-page={overview.ready ? 'overview' : undefined} class="flex flex-col gap-5">
	<PageHeader title="Overview" />
	<FilterBar />
	{#if overview.error}
		<EmptyState text={overview.error} />
	{:else if overview.data}
		<StatStrip {cells} />
		<Section title="Running builds">
			<Table count={running.length} empty="No running builds.">
				{#snippet head()}
					<Th>ID</Th><Th>Project</Th><Th>Pipeline</Th><Th>Ref</Th><Th>State</Th><Th num>Progress</Th><Th num>Started</Th>
				{/snippet}
				{#each running as b (b.id)}
					<Tr>
						<Td><IdLink id={b.id} href={buildHref(b)} /></Td>
						<Td><a href="/p/{b.projectId}" class="hover:underline">{b.projectId}</a></Td>
						<Td>{pipeline(b)}</Td>
						<Td mono>{ref(b)}</Td>
						<Td><StatusBadge state={stateName(b)} /></Td>
						<Td num>{progress(b)}</Td>
						<Td num muted>{ago(b.startedAt ?? b.createdAt)}</Td>
					</Tr>
				{/each}
			</Table>
		</Section>
		<Section title="Recent failures">
			<Table count={failures.length} empty="No recent failures.">
				{#snippet head()}
					<Th>ID</Th><Th>Project</Th><Th>Pipeline</Th><Th>Ref</Th><Th>State</Th><Th>Error</Th><Th num>Created</Th>
				{/snippet}
				{#each failures as b (b.id)}
					<Tr>
						<Td><IdLink id={b.id} href={buildHref(b)} /></Td>
						<Td><a href="/p/{b.projectId}" class="hover:underline">{b.projectId}</a></Td>
						<Td>{pipeline(b)}</Td>
						<Td mono>{ref(b)}</Td>
						<Td><StatusBadge state={stateName(b)} /></Td>
						<Td mono>{errorCode(b)}</Td>
						<Td num muted>{ago(b.createdAt)}</Td>
					</Tr>
				{/each}
			</Table>
		</Section>
	{/if}
</div>
