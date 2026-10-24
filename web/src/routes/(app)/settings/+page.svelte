<script lang="ts">
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { SettingsService, type Settings } from '$lib/gen/bob/settings/v1/settings_pb';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Tabs from '$lib/ui/Tabs.svelte';
	import Forges from './Forges.svelte';
	import General from './General.svelte';
	import Keys from './Keys.svelte';
	import Retention from './Retention.svelte';
	import Webhooks from './Webhooks.svelte';

	const svc = client(SettingsService);
	const tab = $derived(page.url.searchParams.get('tab') ?? 'general');
	const settings = new Query((signal) => svc.getSettings({}, { signal }));

	// save sends the fields named in paths with the etag the form was loaded with.
	async function save(next: Settings, paths: string[]) {
		try {
			const r = await svc.updateSettings({ settings: next, updateMask: { paths } });
			settings.data = { ...settings.data!, settings: r.settings };
			toast.show('Saved.');
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

<svelte:head><title>Settings · bob</title></svelte:head>

<div data-page={settings.ready ? 'settings' : undefined} class="flex flex-col gap-4">
	<PageHeader title="Settings">
		{#snippet meta()}
			{#if settings.data?.settings}
				<span>{settings.data.settings.serverName}</span>
				<span class="font-mono text-sm">{settings.data.settings.baseUrl}</span>
			{/if}
		{/snippet}
	</PageHeader>
	<Tabs
		tabs={[
			{ id: 'general', label: 'General' },
			{ id: 'forges', label: 'Forges' },
			{ id: 'keys', label: 'Signing keys' },
			{ id: 'retention', label: 'Retention' },
			{ id: 'webhooks', label: 'Webhooks' }
		]}
	/>
	{#if settings.error}
		<EmptyState text={settings.error} />
	{:else if tab === 'forges'}
		<Forges />
	{:else if tab === 'keys'}
		<Keys />
	{:else if tab === 'webhooks'}
		<Webhooks />
	{:else if settings.data?.settings}
		{#key settings.data.settings.etag}
			{#if tab === 'retention'}
				<Retention settings={settings.data.settings} {save} />
			{:else}
				<General settings={settings.data.settings} {save} />
			{/if}
		{/key}
	{/if}
</div>
