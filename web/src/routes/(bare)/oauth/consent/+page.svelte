<script lang="ts">
	// OAuth consent for the CLI's PKCE login (SPEC §6.3.2). The server redirects here from
	// /oauth/authorize; the decision goes back as a same-site form POST with the original
	// authorize parameters.
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { Query } from '$lib/query.svelte';
	import { AuthService } from '$lib/gen/bob/auth/v1/auth_pb';
	import Button from '$lib/ui/Button.svelte';

	const auth = client(AuthService);
	const me = new Query((signal) => auth.whoAmI({}, { signal }));
	const params = $derived([...page.url.searchParams].filter(([k]) => k !== 'decision'));
	const clientId = $derived(page.url.searchParams.get('client_id') ?? '');
	const host = $derived(page.url.searchParams.get('host') ?? '');
</script>

<svelte:head><title>Allow access · bob</title></svelte:head>

<form
	method="post"
	action="/oauth/consent"
	data-page={me.ready ? 'consent' : undefined}
	class="flex flex-col gap-4 rounded-xs border border-line p-5"
>
	<h1 class="text-lg font-semibold">Allow access</h1>
	<p>
		Allow <span class="font-mono">{clientId}</span>{host ? ` on ${host}` : ''} to act as
		<span class="font-medium">{me.data?.login ?? 'you'}</span>?
	</p>
	<p class="text-muted">It gets the same permissions as your account until you log out from it.</p>
	{#each params as [k, v] (k)}
		<input type="hidden" name={k} value={v} />
	{/each}
	<div class="flex gap-2">
		<Button variant="primary" type="submit" name="decision" value="allow">Allow</Button>
		<Button type="submit" name="decision" value="deny">Deny</Button>
	</div>
</form>
