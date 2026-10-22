<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { ago, date } from '$lib/format';
	import { Query } from '$lib/query.svelte';
	import { toast } from '$lib/toast.svelte';
	import { AuthService } from '$lib/gen/bob/auth/v1/auth_pb';
	import Button from '$lib/ui/Button.svelte';
	import CodeBlock from '$lib/ui/CodeBlock.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Input from '$lib/ui/Input.svelte';
	import KeyValue from '$lib/ui/KeyValue.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Section from '$lib/ui/Section.svelte';
	import Table from '$lib/ui/Table.svelte';
	import Td from '$lib/ui/Td.svelte';
	import Th from '$lib/ui/Th.svelte';
	import Tr from '$lib/ui/Tr.svelte';

	const auth = client(AuthService);
	const me = new Query((signal) => auth.whoAmI({}, { signal }));
	let version = $state(0);
	const sessions = new Query((signal) => {
		void version;
		return auth.listMySessions({}, { signal });
	});
	const forced = $derived(page.url.searchParams.has('change') || !!me.data?.mustChangePassword);

	let pw = $state({ old: '', next: '', again: '' });
	async function change(e: SubmitEvent) {
		e.preventDefault();
		if (pw.next !== pw.again) return toast.show('The new passwords differ.', true);
		try {
			await auth.changePassword({ oldPassword: pw.old, newPassword: pw.next });
			pw = { old: '', next: '', again: '' };
			toast.show('Password changed. Your other sessions were logged out.');
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function revoke(sessionId: string) {
		try {
			await auth.revokeMySession({ sessionId });
			version++;
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function logout() {
		try {
			await auth.logout({});
		} finally {
			goto('/login');
		}
	}
</script>

<svelte:head><title>Account · bob</title></svelte:head>

<div data-page={me.ready && sessions.ready ? 'account' : undefined} class="flex flex-col gap-6">
	<PageHeader title={me.data?.login ?? 'Account'}>
		{#snippet actions()}
			<Button onclick={logout}>Log out</Button>
		{/snippet}
	</PageHeader>
	{#if me.error}
		<EmptyState text={me.error} />
	{:else if me.data}
		{@const m = me.data}
		<Section title="Identity">
			<KeyValue
				rows={[
					{ k: 'Login', v: m.login, mono: true },
					{ k: 'Principal', v: m.principal?.id ?? '', mono: true },
					{ k: 'Roles', v: m.bindings.map((b) => `${b.role} @ ${b.project}`).join(', ') },
					{ k: 'Session expires', v: date(m.tokenExpiresAt)?.toISOString().replace('T', ' ').slice(0, 16) ?? '' }
				]}
			/>
		</Section>
		{#if m.nix?.substituter}
			<Section title="Binary cache">
				<p class="text-muted">Nix settings for using this server's cache outside bob.</p>
				<CodeBlock text="substituters = {m.nix.substituter}" />
				<CodeBlock text="trusted-public-keys = {m.nix.publicKeys.join(' ')}" />
			</Section>
		{/if}
		<Section title="Change password">
			{#if forced}<p class="text-warn">Choose a new password before you continue.</p>{/if}
			<form class="flex max-w-sm flex-col gap-3" onsubmit={change}>
				<Input label="Current password" type="password" autocomplete="current-password" bind:value={pw.old} required />
				<Input label="New password (12+ characters)" type="password" autocomplete="new-password" minlength={12} bind:value={pw.next} required />
				<Input label="New password again" type="password" autocomplete="new-password" minlength={12} bind:value={pw.again} required />
				<div><Button variant="primary" type="submit">Change password</Button></div>
			</form>
		</Section>
		<Section title="Sessions">
			{#if sessions.error}
				<EmptyState text={sessions.error} />
			{:else}
				<Table count={sessions.data?.sessions.length ?? 0} empty="No sessions.">
					{#snippet head()}
						<Th>Kind</Th><Th>Client</Th><Th num>Created</Th><Th num>Last used</Th><Th></Th>
					{/snippet}
					{#each sessions.data?.sessions ?? [] as s (s.id)}
						<Tr>
							<Td>{s.kind}{s.current ? ' (this one)' : ''}</Td>
							<Td muted>{s.userAgent}</Td>
							<Td num muted>{ago(s.createdAt)}</Td>
							<Td num muted>{ago(s.lastUsedAt)}</Td>
							<Td>
								{#if !s.current}
									<button type="button" class="text-sm text-muted hover:text-fail hover:underline" onclick={() => revoke(s.id)}>revoke</button>
								{/if}
							</Td>
						</Tr>
					{/each}
				</Table>
			{/if}
		</Section>
	{/if}
</div>
