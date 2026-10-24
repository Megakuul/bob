<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { client, errorText } from '$lib/api';
	import { safeNext } from '$lib/next';
	import { Query } from '$lib/query.svelte';
	import { AuthService } from '$lib/gen/bob/auth/v1/auth_pb';
	import Button from '$lib/ui/Button.svelte';
	import Input from '$lib/ui/Input.svelte';

	const auth = client(AuthService);
	const info = new Query((signal) => auth.getServerInfo({}, { signal }));

	const next = $derived(safeNext(page.url.searchParams.get('next')));
	// provider_id "" is the local password login; without server info the form is shown anyway.
	const local = $derived(!info.data || info.data.loginOptions.some((o) => !o.providerId));
	const providers = $derived(info.data?.loginOptions.filter((o) => o.providerId) ?? []);

	let login = $state('');
	let password = $state('');
	let error = $state('');
	let busy = $state(false);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const r = await auth.loginPassword({ login, password });
			goto(r.mustChangePassword ? '/account?change=1' : next, { replaceState: true });
		} catch (err) {
			error = errorText(err);
		} finally {
			busy = false;
		}
	}

	function oidc(url: string): string {
		return url + (url.includes('?') ? '&' : '?') + 'next=' + encodeURIComponent(next);
	}
</script>

<svelte:head><title>Log in · bob</title></svelte:head>

<div data-page={info.ready ? 'login' : undefined} class="flex flex-col gap-4 rounded-xs border border-line p-5">
	{#if info.data?.serverName}
		<p class="text-muted">Log in to {info.data.serverName}</p>
	{/if}
	{#if local}
		<form class="flex flex-col gap-3" onsubmit={submit}>
			<Input label="Login" name="login" autocomplete="username" required bind:value={login} />
			<Input label="Password" name="password" type="password" autocomplete="current-password" required bind:value={password} />
			{#if error}
				<p class="text-fail" role="alert">{error}</p>
			{/if}
			<div><Button variant="primary" type="submit" disabled={busy}>Log in</Button></div>
		</form>
	{/if}
	{#if local && providers.length}
		<div class="flex items-center gap-2 text-sm text-muted">
			<span class="h-px flex-1 bg-line"></span>or<span class="h-px flex-1 bg-line"></span>
		</div>
	{/if}
	{#each providers as p (p.providerId)}
		<Button href={oidc(p.startUrl || `/auth/oidc/start?provider=${p.providerId}`)}>Continue with {p.displayName}</Button>
	{/each}
</div>
