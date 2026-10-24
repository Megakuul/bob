<script lang="ts">
	import { client, errorText } from '$lib/api';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { NotifyService, type NotifyWebhook } from '$lib/gen/bob/notify/v1/notify_pb';
	import Button from '$lib/ui/Button.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import CodeBlock from '$lib/ui/CodeBlock.svelte';
	import Dialog from '$lib/ui/Dialog.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Input from '$lib/ui/Input.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const notify = client(NotifyService);
	let version = $state(0);
	const hooks = new Query((signal) => {
		void version;
		return notify.listNotifyWebhooks({}, { signal });
	});

	let creating = $state(false);
	let f = $state({ url: '', builds: true, releases: false, projects: '' });
	let secret = $state('');

	async function create(e: SubmitEvent) {
		e.preventDefault();
		const events = [f.builds && 'build.finished', f.releases && 'release.published'].filter((x): x is string => !!x);
		const projects = f.projects.split(/[\s,]+/).filter(Boolean);
		try {
			secret = (await notify.createNotifyWebhook({ webhook: { url: f.url, events, projects, enabled: true } })).secret;
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function toggle(h: NotifyWebhook) {
		try {
			await notify.updateNotifyWebhook({ webhook: { ...h, enabled: !h.enabled }, updateMask: { paths: ['enabled'] } });
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function remove(id: string) {
		try {
			await notify.deleteNotifyWebhook({ id });
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

{#if hooks.error}
	<EmptyState text={hooks.error} />
{:else}
	<div class="flex justify-end">
		<Button variant="primary" onclick={() => ((secret = ''), (creating = true))}>Add webhook</Button>
	</div>
	<Table count={hooks.data?.webhooks.length ?? 0} empty="No outgoing webhooks.">
		{#snippet head()}
			<Th>URL</Th><Th>Events</Th><Th>Projects</Th><Th>Enabled</Th><Th>Last result</Th><Th></Th>
		{/snippet}
		{#each hooks.data?.webhooks ?? [] as h (h.id)}
			<Tr>
				<Td mono>{h.url}</Td>
				<Td>{h.events.join(', ')}</Td>
				<Td>{h.projects.join(', ') || 'all'}</Td>
				<Td>{h.enabled ? 'yes' : 'no'}</Td>
				<Td muted>{h.lastResult}</Td>
				<Td>
					<span class="flex gap-3 text-sm">
						<button type="button" class="text-muted hover:text-fg hover:underline" onclick={() => toggle(h)}>{h.enabled ? 'disable' : 'enable'}</button>
						<button type="button" class="text-muted hover:text-fail hover:underline" onclick={() => remove(h.id)}>delete</button>
					</span>
				</Td>
			</Tr>
		{/each}
	</Table>
{/if}

<Dialog bind:open={creating} title="Add webhook">
	{#if secret}
		<p>Signing secret for this webhook. It is not shown again.</p>
		<CodeBlock text={secret} />
		<div class="flex justify-end"><Button onclick={() => ((creating = false), (secret = ''))}>Done</Button></div>
	{:else}
		<form class="flex flex-col gap-3" onsubmit={create}>
			<Input label="URL (https)" type="url" pattern="https://.*" bind:value={f.url} required />
			<Checkbox label="build.finished" bind:checked={f.builds} />
			<Checkbox label="release.published" bind:checked={f.releases} />
			<Input label="Projects (empty = all)" bind:value={f.projects} />
			<div class="flex justify-end gap-2">
				<Button onclick={() => (creating = false)}>Cancel</Button>
				<Button variant="primary" type="submit">Add</Button>
			</div>
		</form>
	{/if}
</Dialog>
