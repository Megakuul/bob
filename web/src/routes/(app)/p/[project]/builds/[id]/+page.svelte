<script lang="ts">
	import { page } from '$app/state';
	import { client, errorText, isCanceled, isUnauthenticated } from '$lib/api';
	import { pipeline, progress, ref, running, sha, stateName, trigger } from '$lib/build';
	import { bytes, date, enumName, span } from '$lib/format';
	import type { GraphNode, Span } from '$lib/graph';
	import { LogFeed } from '$lib/log.svelte';
	import { Query, toLogin } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { BuildService, PackageState, type Build, type PackageNode } from '$lib/gen/bob/build/v1/build_pb';
	import { CacheService } from '$lib/gen/bob/cache/v1/cache_pb';
	import { LogService, type LogLine } from '$lib/gen/bob/log/v1/log_pb';
	import Button from '$lib/ui/Button.svelte';
	import Confirm from '$lib/ui/Confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Graph from '$lib/ui/Graph.svelte';
	import LogView from '$lib/ui/LogView.svelte';
	import Mono from '$lib/ui/Mono.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';
	import Waterfall from '$lib/ui/Waterfall.svelte';
	import { goto } from '$app/navigation';

	const builds = client(BuildService);
	const cache = client(CacheService);
	const logs = client(LogService);

	const project = $derived(page.params.project ?? '');
	const id = $derived(page.params.id ?? '');

	// Build and graph: fetched once, then kept current by WatchBuild while the build runs.
	let build = $state.raw<Build>();
	let nodes = $state.raw<PackageNode[]>([]);
	let error = $state('');
	let loaded = $state(false);

	async function fetchGraph(buildId: string, signal: AbortSignal) {
		nodes = (await builds.getBuildGraph({ buildId }, { signal })).nodes;
	}

	async function watch(buildId: string, signal: AbortSignal) {
		try {
			for await (const m of builds.watchBuild({ id: buildId }, { signal })) {
				if (m.build) build = m.build;
				if (m.changedNodes.some((i) => i >= nodes.length)) await fetchGraph(buildId, signal);
				else if (m.changedNodes.length) {
					const next = nodes.slice();
					m.changedNodes.forEach((i, k) => m.nodes[k] && (next[i] = m.nodes[k]));
					nodes = next;
				}
			}
		} catch {
			// no live updates (stream refused or dropped): the page keeps the last state
		}
	}

	$effect(() => {
		const buildId = id;
		const ctl = new AbortController();
		loaded = false;
		Promise.all([builds.getBuild({ id: buildId }, { signal: ctl.signal }), fetchGraph(buildId, ctl.signal)])
			.then(([r]) => {
				build = r.build;
				error = '';
				if (build && running(build)) watch(buildId, ctl.signal);
			})
			.catch((err) => {
				if (ctl.signal.aborted || isCanceled(err)) return;
				if (isUnauthenticated(err)) return toLogin();
				error = errorText(err);
			})
			.finally(() => (loaded = true));
		return () => ctl.abort();
	});

	const log = new LogFeed(() => id);

	// Packages: table or graph, one row per node; selecting one filters the log.
	let view = $state<'table' | 'graph'>('table');
	let selected = $state('');
	const pkgState = (n: PackageNode) => enumName(PackageState, n.state);
	const graphNodes = $derived<GraphNode[]>(nodes.map((n) => ({ label: n.package?.label ?? '', state: pkgState(n), deps: n.deps })));
	const tooBig = $derived(nodes.length > 300);
	const spans = $derived<Span[]>(
		nodes
			.filter((n) => n.startedAt)
			.map((n) => ({
				label: n.package?.label ?? '',
				state: pkgState(n),
				start: date(n.startedAt)!.getTime(),
				end: date(n.finishedAt)?.getTime()
			}))
	);
	const arch = (p = '') => p.replace('linux/', '');

	// Artifacts: outputs of the build's targets, or of the packages nothing depends on.
	const artifacts = $derived.by(() => {
		const used = new Set(nodes.flatMap((n) => n.deps));
		const targets = new Set(build?.targets ?? []);
		return nodes
			.filter((n, i) => (targets.size ? targets.has(n.package?.label ?? '') : !used.has(i)))
			.flatMap((n) => n.outputs)
			.slice(0, 20);
	});
	// keyed by the joined paths so a WatchBuild update that changes nothing here fetches nothing
	const artifactKey = $derived(artifacts.join(' '));
	const sizes = new Query(async (signal) => {
		const out: Record<string, bigint> = {};
		await Promise.all(
			artifactKey.split(' ').filter(Boolean).map(async (p) => {
				const hash = p.replace('/nix/store/', '').slice(0, 32);
				try {
					out[p] = (await cache.getPath({ storeHash: hash }, { signal })).narSize;
				} catch {
					// size unknown (not promoted yet): the path is still shown
				}
			})
		);
		return out;
	});

	// Log
	let follow = $state(true);
	let term = $state('');
	let hits = $state.raw<LogLine[] | undefined>();
	const shown = $derived(
		hits
			? hits.map((l) => ({ key: `${l.package}#${l.attempt}#${l.seq}`, package: l.package, seq: l.seq, attempt: l.attempt, text: l.text }))
			: selected
				? log.lines.filter((l) => l.package === selected)
				: log.lines
	);

	async function search(e: SubmitEvent) {
		e.preventDefault();
		if (term.length < 3) {
			hits = undefined;
			return;
		}
		try {
			hits = (await logs.searchLog({ buildId: id, term, package: selected })).hits;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	// Actions
	let cancelling = $state(false);
	async function act(kind: 'cancel' | 'retry' | 'rerun') {
		try {
			if (kind === 'cancel') {
				const r = await builds.cancelBuild({ id });
				build = r.build;
				return;
			}
			const r = kind === 'retry' ? await builds.retryBuild({ id }) : await builds.rerunBuild({ id });
			if (r.build) goto(`/p/${r.build.projectId}/builds/${r.build.id}`);
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

<svelte:head><title>Build {build ? '#' + build.number : id} · bob</title></svelte:head>

<div data-page={loaded ? 'build' : undefined} class="flex flex-col gap-5">
	<PageHeader
		title={build ? `#${build.number}` : 'Build'}
		crumbs={[
			{ label: 'Projects', href: '/projects' },
			{ label: project, href: `/p/${project}` }
		]}
	>
		{#snippet meta()}
			<span class="font-mono text-sm">{id}</span>
			{#if build}
				<span>{pipeline(build)}</span>
				<span class="font-mono text-sm">{ref(build)} {sha(build)}</span>
				<span>{trigger(build)}</span>
				<StatusBadge state={stateName(build)} />
			{/if}
		{/snippet}
		{#snippet actions()}
			{#if build}
				{#if running(build)}
					<Button variant="danger" onclick={() => (cancelling = true)}>Cancel</Button>
				{:else}
					<Button onclick={() => act('retry')}>Retry</Button>
				{/if}
				<Button onclick={() => act('rerun')}>Re-run</Button>
			{/if}
		{/snippet}
	</PageHeader>

	{#if error}
		<EmptyState text={error} />
	{:else if build}
		<p class="text-muted">
			platforms <span class="font-mono text-sm text-fg">{build.platforms.join(' ')}</span>
			{#if build.variants.length}· variant <span class="text-fg">{build.variants.join(', ')}</span>{/if}
			· attempt <span class="text-fg">{build.attempt}</span>
			· <span class="text-fg tabular-nums">{progress(build)}</span>
			· cached <span class="text-fg tabular-nums">{build.counts?.cached ?? 0}</span>
			{#if build.startedAt}· duration <span class="text-fg tabular-nums">{span(build.startedAt, build.finishedAt)}</span>{/if}
		</p>

		<div class="grid gap-5 lg:grid-cols-[minmax(0,5fr)_minmax(0,6fr)]">
			<div data-section="packages">
				<Section title="Packages">
					{#snippet actions()}
						<div class="inline-flex rounded-xs border border-line text-sm" role="group" aria-label="view">
							<button type="button" data-view="table" aria-pressed={view === 'table'} onclick={() => (view = 'table')} class="px-2 py-0.5 {view === 'table' ? 'bg-surface text-fg' : 'text-muted hover:text-fg'}">table</button>
							<button type="button" data-view="graph" aria-pressed={view === 'graph'} disabled={tooBig} title={tooBig ? `graph has ${nodes.length} packages; use the table` : undefined} onclick={() => (view = 'graph')} class="px-2 py-0.5 disabled:opacity-40 {view === 'graph' ? 'bg-surface text-fg' : 'text-muted hover:text-fg'}">graph</button>
						</div>
					{/snippet}
					{#if view === 'graph' && !tooBig}
						<Graph nodes={graphNodes} onselect={(l) => (selected = selected === l ? '' : l)} />
					{:else}
						<Table count={nodes.length} empty="No packages yet (evaluating).">
							{#snippet head()}
								<Th>Package</Th><Th>Platform</Th><Th>State</Th><Th num>Time</Th>
							{/snippet}
							{#each nodes as n, i (i)}
								{@const label = n.package?.label ?? ''}
								<Tr selected={selected === label} onclick={() => (selected = selected === label ? '' : label)}>
									<Td mono>{label}</Td>
									<Td mono muted>{arch(n.package?.platform)}</Td>
									<Td><StatusBadge state={pkgState(n)} /></Td>
									<Td num>{span(n.startedAt, n.finishedAt)}</Td>
								</Tr>
								{#if n.error}
									<Tr>
										<Td colspan={4}>
											<span class="text-muted">└</span>
											<span class="font-mono text-sm text-fail">{n.error.code}</span>
											{n.error.message}
											<span class="font-mono text-sm text-muted">{n.error.package?.definedAt || n.package?.definedAt}</span>
										</Td>
									</Tr>
								{/if}
							{/each}
						</Table>
					{/if}
				</Section>
			</div>

			<div data-section="log">
				<Section title="Log">
					{#snippet actions()}
						<div class="flex flex-wrap items-center gap-2 text-sm">
							{#if selected}
								<span class="font-mono">{selected}</span>
								<button type="button" class="text-muted hover:text-fg hover:underline" onclick={() => (selected = '')}>all packages</button>
							{/if}
							<label class="inline-flex items-center gap-1 text-muted">
								<input type="checkbox" bind:checked={follow} class="accent-fg" /> follow
							</label>
							<form onsubmit={search}>
								<input
									type="search"
									bind:value={term}
									placeholder="search"
									aria-label="search log"
									class="h-6 w-36 rounded-xs border border-line bg-bg px-2 text-sm text-fg placeholder:text-muted"
								/>
							</form>
						</div>
					{/snippet}
					<div class="border border-line">
						{#if log.truncated}<p class="border-b border-line px-2 py-1 text-sm text-muted">Earlier lines are not shown.</p>{/if}
						{#if log.delayed}<p class="border-b border-line px-2 py-1 text-sm text-warn">Live log delayed: showing stored lines only.</p>{/if}
						{#if hits}
							<p class="border-b border-line px-2 py-1 text-sm text-muted">
								{hits.length} matches for “{term}” <button type="button" class="hover:text-fg hover:underline" onclick={() => (hits = undefined)}>clear</button>
							</p>
						{/if}
						{#if log.error}
							<EmptyState text={log.error} />
						{:else}
							<LogView lines={shown} follow={follow && !hits} showPackage={!selected} />
						{/if}
					</div>
				</Section>
			</div>
		</div>

		<div data-section="waterfall">
			<Section title="Timing waterfall">
				{#if spans.length}
					<Waterfall {spans} />
				{:else}
					<EmptyState text="No package has started yet." />
				{/if}
			</Section>
		</div>

		<div data-section="artifacts">
			<Section title="Artifacts">
				<Table count={artifacts.length} empty="No artifacts yet.">
					{#snippet head()}
						<Th>Store path</Th><Th num>Size</Th>
					{/snippet}
					{#each artifacts as p (p)}
						<Tr>
							<Td><Mono text={p} copy /></Td>
							<Td num>{sizes.data?.[p] !== undefined ? bytes(sizes.data[p]) : ''}</Td>
						</Tr>
					{/each}
				</Table>
			</Section>
		</div>
	{/if}
</div>

<Confirm
	bind:open={cancelling}
	title="Cancel build?"
	text="Running packages are stopped; finished packages stay in the cache."
	confirm="Cancel build"
	danger
	onconfirm={() => act('cancel')}
/>
