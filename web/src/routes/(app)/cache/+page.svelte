<script lang="ts">
	import { client, errorText } from '$lib/api';
	import { ago, bytes, percent } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { CacheService, type GetPathResponse } from '$lib/gen/bob/cache/v1/cache_pb';
	import Button from '$lib/ui/Button.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Input from '$lib/ui/Input.svelte';
	import KeyValue from '$lib/ui/KeyValue.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatStrip from '$lib/ui/StatStrip.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const cache = client(CacheService);
	const stats = new Query((signal) => cache.getCacheStats({}, { signal }), { refresh: true });
	const top = new Query((signal) => cache.listTopPackages({ limit: 50 }, { signal }), { refresh: true });

	const cells = $derived.by(() => {
		const s = stats.data;
		if (!s) return [];
		return [
			{ label: 'Size', value: bytes(s.narBytes) },
			{ label: 'Paths', value: s.paths.toLocaleString('en') },
			{ label: 'Hit rate 24 h', value: percent(s.hitRate24h) },
			{ label: 'Hit rate 7 d', value: percent(s.hitRate7d) },
			{ label: 'Retention (days)', value: String(s.retentionUnreferencedDays) }
		];
	});

	let hash = $state('');
	let found = $state<GetPathResponse>();
	let lookupError = $state('');

	async function lookup(e: SubmitEvent) {
		e.preventDefault();
		// accept a bare hash or a whole store path
		const h = hash.trim().replace(/^\/nix\/store\//, '').slice(0, 32);
		found = undefined;
		lookupError = '';
		try {
			found = await cache.getPath({ storeHash: h });
		} catch (err) {
			lookupError = errorText(err);
		}
	}
</script>

<svelte:head><title>Cache · bob</title></svelte:head>

<div data-page={stats.ready && top.ready ? 'cache' : undefined} class="flex flex-col gap-5">
	<PageHeader title="Cache" />
	{#if stats.error}
		<EmptyState text={stats.error} />
	{:else}
		<StatStrip {cells} />
		{#if stats.data?.measuredAt}<p class="text-sm text-muted">Size and paths measured {ago(stats.data.measuredAt)} ago.</p>{/if}
	{/if}
	<Section title="Top packages (7 days)">
		{#if top.error}
			<EmptyState text={top.error} />
		{:else}
			<Table count={top.data?.packages.length ?? 0} empty="No cache hits yet.">
				{#snippet head()}
					<Th>Project</Th><Th>Label</Th><Th num>Hits 7 d</Th><Th num>Size</Th>
				{/snippet}
				{#each top.data?.packages ?? [] as p (p.projectId + p.label)}
					<Tr>
						<Td><a href="/p/{p.projectId}" class="hover:underline">{p.projectId}</a></Td>
						<Td mono>{p.label}</Td>
						<Td num>{p.hits7d.toLocaleString('en')}</Td>
						<Td num>{bytes(p.narBytes)}</Td>
					</Tr>
				{/each}
			</Table>
		{/if}
	</Section>
	<Section title="Path lookup">
		<form class="flex flex-wrap items-end gap-2" onsubmit={lookup}>
			<Input label="Store hash or path" bind:value={hash} required spellcheck={false} />
			<Button type="submit">Look up</Button>
		</form>
		{#if lookupError}
			<p class="text-fail">{lookupError}</p>
		{:else if found}
			<KeyValue
				rows={[
					{ k: 'Store path', v: found.storePath, mono: true },
					{ k: 'NAR size', v: bytes(found.narSize) },
					{ k: 'File size', v: bytes(found.fileSize) },
					{ k: 'References', v: String(found.references.length) },
					{ k: 'Signatures', v: found.signatures.join(' '), mono: true },
					{ k: 'Projects', v: found.projects.join(', ') },
					{ k: 'Public', v: found.public ? 'yes' : 'no' },
					{ k: 'First build', v: found.buildId, mono: true },
					{ k: 'Promoted', v: found.promotedAt ? ago(found.promotedAt) + ' ago' : '' }
				]}
			/>
		{/if}
	</Section>
</div>
