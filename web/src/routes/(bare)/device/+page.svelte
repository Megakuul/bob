<script lang="ts">
	// Device flow approval (RFC 8628, SPEC §6.3.2): needs a browser session; the decision is a
	// same-site form POST to /device, answered by the server.
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { Query } from '$lib/query.svelte';
	import { AuthService } from '$lib/gen/bob/auth/v1/auth_pb';
	import Button from '$lib/ui/Button.svelte';
	import Input from '$lib/ui/Input.svelte';

	const auth = client(AuthService);
	const me = new Query((signal) => auth.whoAmI({}, { signal }));
	let code = $state(page.url.searchParams.get('user_code') ?? '');
</script>

<svelte:head><title>Connect a device · bob</title></svelte:head>

<form
	method="post"
	action="/device"
	data-page={me.ready ? 'device' : undefined}
	class="flex flex-col gap-4 rounded-xs border border-line p-5"
>
	<h1 class="text-lg font-semibold">Connect a device</h1>
	<p class="text-muted">
		Enter the code shown by <span class="font-mono">bob login --device</span>. Approving lets that
		device act as {me.data?.login ?? 'you'}.
	</p>
	<Input label="Code" name="user_code" autocomplete="off" spellcheck={false} required bind:value={code} />
	<p class="font-mono text-xl tracking-widest">{code.toUpperCase()}</p>
	<div class="flex gap-2">
		<Button variant="primary" type="submit" name="decision" value="approve">Approve</Button>
		<Button type="submit" name="decision" value="deny">Deny</Button>
	</div>
</form>
