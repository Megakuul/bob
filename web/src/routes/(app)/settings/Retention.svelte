<script lang="ts">
	import { untrack } from 'svelte';
	import type { Settings } from '$lib/gen/bob/settings/v1/settings_pb';
	import Button from '$lib/ui/Button.svelte';
	import Input from '$lib/ui/Input.svelte';

	let { settings, save }: { settings: Settings; save: (s: Settings, paths: string[]) => void } = $props();

	const r = untrack(() => settings.retention);
	let f = $state({
		logs: String(r?.logsDays ?? 30),
		analytics: String(r?.analyticsDays ?? 400),
		builds: String(r?.buildsDays ?? 365),
		cache: String(r?.cacheUnreferencedDays ?? 90),
		staging: String(r?.stagingDays ?? 7)
	});

	function submit(e: SubmitEvent) {
		e.preventDefault();
		save(
			{
				...settings,
				retention: {
					$typeName: 'bob.settings.v1.Retention',
					logsDays: Number(f.logs),
					analyticsDays: Number(f.analytics),
					buildsDays: Number(f.builds),
					cacheUnreferencedDays: Number(f.cache),
					stagingDays: Number(f.staging)
				}
			},
			['retention']
		);
	}
</script>

<form class="grid max-w-xl grid-cols-2 gap-3" onsubmit={submit}>
	<Input label="Logs (days)" type="number" min="1" bind:value={f.logs} />
	<Input label="Analytics (days)" type="number" min="1" bind:value={f.analytics} />
	<Input label="Builds and jobs (days)" type="number" min="1" bind:value={f.builds} />
	<Input label="Unreferenced cache paths (days)" type="number" min="1" bind:value={f.cache} />
	<Input label="Upload staging (days)" type="number" min="1" bind:value={f.staging} />
	<div class="col-span-2"><Button variant="primary" type="submit">Save</Button></div>
</form>
