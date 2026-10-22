<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { enumName } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { BuildService } from '$lib/gen/bob/build/v1/build_pb';
	import { LinkHealth, ProjectService } from '$lib/gen/bob/project/v1/project_pb';
	import Button from '$lib/ui/Button.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Input from '$lib/ui/Input.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Select from '$lib/ui/Select.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Tabs from '$lib/ui/Tabs.svelte';
	import Builds from './Builds.svelte';
	import Permissions from './Permissions.svelte';
	import Pipelines from './Pipelines.svelte';
	import Releases from './Releases.svelte';
	import Settings from './Settings.svelte';

	const projects = client(ProjectService);
	const builds = client(BuildService);
	const id = $derived(page.params.project ?? '');
	const project = new Query((signal) => projects.getProject({ id }, { signal }));
	const tab = $derived(page.url.searchParams.get('tab') ?? 'builds');

	const tabs = [
		{ id: 'builds', label: 'Builds' },
		{ id: 'pipelines', label: 'Pipelines' },
		{ id: 'releases', label: 'Releases' },
		{ id: 'settings', label: 'Settings' },
		{ id: 'permissions', label: 'Permissions' }
	];

	let running = $state(false);
	let pipelines = $state<{ value: string; label: string }[]>([]);
	let pipeline = $state('');
	let ref = $state('');

	async function openRun() {
		ref = project.data?.project?.defaultBranch ?? '';
		running = true;
		try {
			const r = await projects.listPipelines({ projectId: id });
			pipelines = r.pipelines.map((p) => ({ value: p.name, label: p.name }));
			pipeline = pipelines[0]?.value ?? '';
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function run(e: SubmitEvent) {
		e.preventDefault();
		try {
			const r = await builds.createBuild({
				projectId: id,
				pipeline,
				source: { kind: { case: 'git', value: { ref } } }
			});
			running = false;
			if (r.build) goto(`/p/${id}/builds/${r.build.id}`);
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

<svelte:head><title>{id} · bob</title></svelte:head>

<div data-page={project.ready ? 'project' : undefined} class="flex flex-col gap-4">
	<PageHeader title={id} crumbs={[{ label: 'Projects', href: '/projects' }]}>
		{#snippet meta()}
			{#if project.data?.project}
				{@const p = project.data.project}
				{#if p.displayName && p.displayName !== id}<span>{p.displayName}</span>{/if}
				{#if p.repoWebUrl}
					<a href={p.repoWebUrl} class="font-mono text-sm hover:underline">{p.repoFullName}</a>
				{:else}
					<span class="font-mono text-sm">{p.repoFullName}</span>
				{/if}
				<span class="inline-flex items-center gap-1">link <StatusBadge state={enumName(LinkHealth, p.linkHealth)} /></span>
			{/if}
		{/snippet}
		{#snippet actions()}
			<Button variant="primary" onclick={openRun}>Run pipeline</Button>
		{/snippet}
	</PageHeader>
	{#if project.error}
		<EmptyState text={project.error} />
	{:else}
		<Tabs {tabs} />
		{#if tab === 'pipelines'}
			<Pipelines project={id} />
		{:else if tab === 'releases'}
			<Releases project={id} />
		{:else if tab === 'settings'}
			{#if project.data?.project}<Settings project={project.data.project} />{/if}
		{:else if tab === 'permissions'}
			<Permissions project={id} />
		{:else}
			<Builds project={id} />
		{/if}
	{/if}
</div>

<Dialog bind:open={running} title="Run pipeline">
	<form class="flex flex-col gap-3" onsubmit={run}>
		{#if pipelines.length}
			<Select label="Pipeline" bind:value={pipeline} options={pipelines} />
		{:else}
			<p class="text-muted">This project has no pipelines.</p>
		{/if}
		<Input label="Branch, tag or ref" bind:value={ref} required />
		<div class="flex justify-end gap-2">
			<Button onclick={() => (running = false)}>Cancel</Button>
			<Button variant="primary" type="submit" disabled={!pipeline}>Run</Button>
		</div>
	</form>
</Dialog>
