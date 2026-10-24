<script lang="ts">
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { ago, bytes, enumName } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { AuthService } from '$lib/gen/bob/auth/v1/auth_pb';
	import { Visibility } from '$lib/gen/bob/common/v1/common_pb';
	import { ReleaseService, ReleaseState, AssetKind } from '$lib/gen/bob/release/v1/release_pb';
	import Button from '$lib/ui/Button.svelte';
	import CodeBlock from '$lib/ui/CodeBlock.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import IdLink from '$lib/ui/IdLink.svelte';
	import Mono from '$lib/ui/Mono.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Section from '$lib/ui/Section.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const releases = client(ReleaseService);
	const project = $derived(page.params.project ?? '');
	const name = $derived(page.params.name ?? '');
	const version = $derived(page.params.version ?? '');
	// Releases are addressed by (project, name, version); ResolveRelease with an exact version
	// is the lookup the API offers for that.
	const res = new Query((signal) => releases.resolveRelease({ projectId: project, name, constraint: version }, { signal }));
	let release = $derived(res.data?.release);
	// image refs are pulled from the server's OCI registry host
	const info = new Query((signal) => client(AuthService).getServerInfo({}, { signal }));
	const registry = $derived(info.data?.ociRegistry ? info.data.ociRegistry + '/' : '');

	async function setVisibility(v: Visibility) {
		if (!release) return;
		try {
			const r = await releases.updateRelease({ release: { ...release, visibility: v }, updateMask: { paths: ['visibility'] } });
			release = r.release;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

<svelte:head><title>{name} {version} · bob</title></svelte:head>

<div data-page={res.ready && info.ready ? 'release' : undefined} class="flex flex-col gap-5">
	<PageHeader
		title="{name} {version}"
		crumbs={[
			{ label: 'Projects', href: '/projects' },
			{ label: project, href: `/p/${project}?tab=releases` }
		]}
	>
		{#snippet meta()}
			{#if release}
				<StatusBadge state={enumName(ReleaseState, release.state)} />
				<span>{enumName(Visibility, release.visibility).toLowerCase()}</span>
				{#if release.buildId}<span>build <IdLink id={release.buildId} href="/p/{project}/builds/{release.buildId}" /></span>{/if}
				<span class="font-mono text-sm">{release.sha.slice(0, 7)}</span>
				<span>{ago(release.createdAt)} ago</span>
			{/if}
		{/snippet}
		{#snippet actions()}
			{#if release}
				{#if release.visibility === Visibility.PUBLIC}
					<Button onclick={() => setVisibility(Visibility.PRIVATE)}>Make private</Button>
				{:else}
					<Button onclick={() => setVisibility(Visibility.PUBLIC)}>Make public</Button>
				{/if}
			{/if}
		{/snippet}
	</PageHeader>
	{#if res.error}
		<EmptyState text={res.error} />
	{:else if release}
		<Section title="Install">
			<CodeBlock text="bob install {release.projectId}/{release.name}@{release.version}" />
		</Section>
		{#if release.description}
			<p class="max-w-3xl whitespace-pre-wrap">{release.description}</p>
		{/if}
		<Section title="Assets">
			<Table count={release.assets.length} empty="No downloadable assets.">
				{#snippet head()}
					<Th>File</Th><Th>Kind</Th><Th>Platform</Th><Th num>Size</Th><Th>SHA-256</Th>
				{/snippet}
				{#each release.assets as a (a.file)}
					<Tr>
						<Td mono><a href={a.url} class="hover:underline" download>{a.file}</a></Td>
						<Td>{enumName(AssetKind, a.kind).toLowerCase()}</Td>
						<Td mono>{a.platform}</Td>
						<Td num>{bytes(a.size)}</Td>
						<Td><Mono text={a.sha256} copy /></Td>
					</Tr>
				{/each}
			</Table>
		</Section>
		<Section title="Closure roots">
			<Table count={release.closures.length} empty="Nothing to install.">
				{#snippet head()}
					<Th>Platform</Th><Th>Roots</Th><Th num>Closure</Th>
				{/snippet}
				{#each release.closures as c (c.platform)}
					<Tr>
						<Td mono>{c.platform}</Td>
						<Td mono>{c.roots.join(' ')}</Td>
						<Td num>{bytes(c.closureNarBytes)}</Td>
					</Tr>
				{/each}
			</Table>
		</Section>
		<Section title="Images">
			{#if release.images.length}
				<div class="flex flex-col gap-2">
					{#each release.images as im (im.repository + im.tag)}
						<CodeBlock text="podman pull {registry}{im.repository}{im.tag ? ':' + im.tag : '@' + im.digest}" />
					{/each}
				</div>
			{:else}
				<EmptyState text="No images." />
			{/if}
		</Section>
	{/if}
</div>
