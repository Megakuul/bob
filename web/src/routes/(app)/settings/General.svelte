<script lang="ts">
	import { untrack } from 'svelte';
	import type { Settings } from '$lib/gen/bob/settings/v1/settings_pb';
	import Button from '$lib/ui/Button.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import Input from '$lib/ui/Input.svelte';
	import Section from '$lib/ui/Section.svelte';

	let { settings, save }: { settings: Settings; save: (s: Settings, paths: string[]) => void } = $props();

	// numbers are edited as text and converted on save
	const init = untrack(() => settings);
	let f = $state({
		serverName: init.serverName,
		baseUrl: init.baseUrl,
		liveLog: init.liveLogEnabled,
		downloads: String(init.maxParallelDownloads),
		cliIdle: String(init.sessions?.cliIdleDays ?? 7),
		cliMax: String(init.sessions?.cliMaxDays ?? 30),
		browserIdle: String(init.sessions?.browserIdleHours ?? 12),
		browserMax: String(init.sessions?.browserMaxDays ?? 7),
		saMax: String(init.sessions?.saTokenMaxDays ?? 365)
	});

	function submit(e: SubmitEvent) {
		e.preventDefault();
		save(
			{
				...settings,
				serverName: f.serverName,
				baseUrl: f.baseUrl,
				liveLogEnabled: f.liveLog,
				maxParallelDownloads: Number(f.downloads),
				sessions: {
					$typeName: 'bob.settings.v1.SessionPolicy',
					cliIdleDays: Number(f.cliIdle),
					cliMaxDays: Number(f.cliMax),
					browserIdleHours: Number(f.browserIdle),
					browserMaxDays: Number(f.browserMax),
					saTokenMaxDays: Number(f.saMax)
				}
			},
			['server_name', 'base_url', 'live_log_enabled', 'max_parallel_downloads', 'sessions']
		);
	}
</script>

<form class="flex max-w-xl flex-col gap-6" onsubmit={submit}>
	<Section title="Server">
		<Input label="Server name (signing key prefix)" bind:value={f.serverName} required />
		<Input label="Base URL" type="url" bind:value={f.baseUrl} required />
		<Input label="Parallel downloads per orchestrator" type="number" min="1" bind:value={f.downloads} />
		<Checkbox label="Live logs from workers" bind:checked={f.liveLog} />
	</Section>
	<Section title="Session policy">
		<div class="grid grid-cols-2 gap-3">
			<Input label="CLI idle (days)" type="number" min="1" bind:value={f.cliIdle} />
			<Input label="CLI max (days)" type="number" min="1" bind:value={f.cliMax} />
			<Input label="Browser idle (hours)" type="number" min="1" bind:value={f.browserIdle} />
			<Input label="Browser max (days)" type="number" min="1" bind:value={f.browserMax} />
			<Input label="Service account token max (days)" type="number" min="1" bind:value={f.saMax} />
		</div>
	</Section>
	<div><Button variant="primary" type="submit">Save</Button></div>
</form>
