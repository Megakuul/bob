<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { ago, duration, matches } from '$lib/format';
	import { buildHref, errorCode, pipeline, stateName } from '$lib/build';
	import { Query } from '$lib/query.svelte';
	import { AnalyticsService } from '$lib/gen/bob/analytics/v1/analytics_pb';
	import { ProjectService } from '$lib/gen/bob/project/v1/project_pb';
	import BarChart from '$lib/ui/BarChart.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import FilterBar from '$lib/ui/FilterBar.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import LineChart from '$lib/ui/LineChart.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const analytics = client(AnalyticsService);
	const projects = client(ProjectService);
	const project = $derived(page.url.searchParams.get('project') ?? '');
	const days = 30;

	const list = new Query((signal) => projects.listProjects({ pageSize: 500 }, { signal }));
	const history = new Query((signal) => analytics.buildHistory({ projectId: project, days }, { signal }));
	const durations = new Query((signal) => analytics.packageDurations({ projectId: project, days }, { signal }));
	const failures = new Query((signal) => analytics.recentFailures({ projectId: project, limit: 20 }, { signal }));

	const rows = $derived(
		(history.data?.days ?? []).map((d) => ({
			x: d.day,
			v: { succeeded: d.succeeded, failed: d.failed, cancelled: d.cancelled, p50: d.p50DurationS / 60, p90: d.p90DurationS / 60 }
		}))
	);

	// Slowest packages: per-day averages folded into one run-weighted average per label.
	const slowest = $derived.by(() => {
		const m = new Map<string, { ms: number; runs: number }>();
		for (const r of durations.data?.rows ?? []) {
			const a = m.get(r.label) ?? { ms: 0, runs: 0 };
			const n = Number(r.count);
			m.set(r.label, { ms: a.ms + r.avgMs * n, runs: a.runs + n });
		}
		return [...m]
			.map(([label, a]) => ({ label, avg: a.runs ? a.ms / a.runs : 0, runs: a.runs }))
			.filter((p) => matches(page.url.searchParams.get('query') ?? '', p.label))
			.sort((a, b) => b.avg - a.avg)
			.slice(0, 50);
	});

	function choose(e: Event) {
		const u = new URL(page.url);
		const v = (e.currentTarget as HTMLSelectElement).value;
		if (v) u.searchParams.set('project', v);
		else u.searchParams.delete('project');
		goto(u, { replaceState: true, keepFocus: true, noScroll: true });
	}
</script>

<svelte:head><title>Analytics · bob</title></svelte:head>

<div data-page={history.ready && durations.ready && failures.ready ? 'analytics' : undefined} class="flex flex-col gap-5">
	<PageHeader title="Analytics">
		{#snippet actions()}
			<label class="flex items-center gap-1 text-sm text-muted">
				project
				<select value={project} onchange={choose} class="h-7 rounded-xs border border-line bg-bg px-1 text-base text-fg">
					<option value="">all</option>
					{#each list.data?.projects ?? [] as p (p.id)}<option value={p.id}>{p.id}</option>{/each}
				</select>
			</label>
		{/snippet}
	</PageHeader>
	<div class="grid gap-5 lg:grid-cols-2">
		<Section title="Builds per day ({days} days)">
			{#if history.error}
				<EmptyState text={history.error} />
			{:else}
				<BarChart
					{rows}
					series={[
						{ key: 'succeeded', label: 'succeeded', tone: 'ok' },
						{ key: 'failed', label: 'failed', tone: 'fail' },
						{ key: 'cancelled', label: 'cancelled', tone: 'idle' }
					]}
				/>
			{/if}
		</Section>
		<Section title="Build duration">
			{#if history.error}
				<EmptyState text={history.error} />
			{:else}
				<LineChart
					{rows}
					unit=" min"
					series={[
						{ key: 'p50', label: 'p50' },
						{ key: 'p90', label: 'p90', dashed: true }
					]}
				/>
			{/if}
		</Section>
	</div>
	<Section title="Slowest packages">
		{#if durations.error}
			<EmptyState text={durations.error} />
		{:else}
			<FilterBar placeholder="filter labels" />
			<Table count={slowest.length} empty="No package timings yet.">
				{#snippet head()}
					<Th>Label</Th><Th num>Average</Th><Th num>Runs</Th>
				{/snippet}
				{#each slowest as p (p.label)}
					<Tr>
						<Td mono>{p.label}</Td>
						<Td num>{duration(p.avg)}</Td>
						<Td num>{p.runs}</Td>
					</Tr>
				{/each}
			</Table>
		{/if}
	</Section>
	<Section title="Recent failures">
		{#if failures.error}
			<EmptyState text={failures.error} />
		{:else}
			<Table count={failures.data?.builds.length ?? 0} empty="No failures.">
				{#snippet head()}
					<Th>ID</Th><Th>Project</Th><Th>Pipeline</Th><Th>State</Th><Th>Error</Th><Th num>Created</Th>
				{/snippet}
				{#each failures.data?.builds ?? [] as b (b.id)}
					<Tr>
						<Td><IdLink id={b.id} href={buildHref(b)} /></Td>
						<Td><a href="/p/{b.projectId}" class="hover:underline">{b.projectId}</a></Td>
						<Td>{pipeline(b)}</Td>
						<Td><StatusBadge state={stateName(b)} /></Td>
						<Td mono>{errorCode(b)}</Td>
						<Td num muted>{ago(b.createdAt)}</Td>
					</Tr>
				{/each}
			</Table>
		{/if}
	</Section>
</div>
