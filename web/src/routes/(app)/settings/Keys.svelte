<script lang="ts">
	import { client, errorText } from '$lib/api';
	import { ago, enumName } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { KeyState, SettingsService } from '$lib/gen/bob/settings/v1/settings_pb';
	import Button from '$lib/ui/Button.svelte';
	import Confirm from '$lib/ui/Confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Mono from '$lib/ui/Mono.svelte';
	import StatusBadge from '$lib/ui/StatusBadge.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const svc = client(SettingsService);
	let version = $state(0);
	const keys = new Query((signal) => {
		void version;
		return svc.listSigningKeys({}, { signal });
	});

	async function create() {
		try {
			await svc.createSigningKey({});
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	let retiring = $state('');
	let confirming = $state(false);
	async function retire() {
		try {
			await svc.retireSigningKey({ name: retiring });
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

{#if keys.error}
	<EmptyState text={keys.error} />
{:else}
	<div class="flex justify-end"><Button onclick={create}>Rotate (create new key)</Button></div>
	<Table count={keys.data?.keys.length ?? 0} empty="No signing keys.">
		{#snippet head()}
			<Th>Name</Th><Th>State</Th><Th>Public key</Th><Th num>Created</Th><Th></Th>
		{/snippet}
		{#each keys.data?.keys ?? [] as k (k.name)}
			<Tr>
				<Td mono>{k.name}</Td>
				<Td><StatusBadge state={enumName(KeyState, k.state)} /></Td>
				<Td><Mono text={k.publicKey} copy /></Td>
				<Td num muted>{ago(k.createdAt)}</Td>
				<Td>
					{#if k.state === KeyState.ACTIVE || k.state === KeyState.VERIFY_ONLY}
						<button type="button" class="text-sm text-muted hover:text-fail hover:underline" onclick={() => ((retiring = k.name), (confirming = true))}>retire</button>
					{/if}
				</Td>
			</Tr>
		{/each}
	</Table>
{/if}

<Confirm
	bind:open={confirming}
	title="Retire {retiring}?"
	text="Paths signed only by this key are re-signed with the active key; clients that trust only this key stop accepting new paths."
	confirm="Retire"
	danger
	onconfirm={retire}
/>
