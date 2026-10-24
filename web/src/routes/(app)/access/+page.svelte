<script lang="ts">
	import { page } from '$app/state';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Tabs from '$lib/ui/Tabs.svelte';
	import Matrix from './Matrix.svelte';
	import Oidc from './Oidc.svelte';
	import Roles from './Roles.svelte';
	import ServiceAccounts from './ServiceAccounts.svelte';
	import Users from './Users.svelte';

	const tab = $derived(page.url.searchParams.get('tab') ?? 'users');
	// each tab reports when its first data arrived, so data-page marks a settled page
	let ready = $state(false);
</script>

<svelte:head><title>Access · bob</title></svelte:head>

<div data-page={ready ? 'access' : undefined} class="flex flex-col gap-4">
	<PageHeader title="Access" />
	<Tabs
		tabs={[
			{ id: 'users', label: 'Users' },
			{ id: 'roles', label: 'Roles' },
			{ id: 'matrix', label: 'Matrix' },
			{ id: 'service-accounts', label: 'Service accounts' },
			{ id: 'oidc', label: 'OIDC' }
		]}
	/>
	{#if tab === 'roles'}
		<Roles bind:ready />
	{:else if tab === 'matrix'}
		<Matrix bind:ready />
	{:else if tab === 'service-accounts'}
		<ServiceAccounts bind:ready />
	{:else if tab === 'oidc'}
		<Oidc bind:ready />
	{:else}
		<Users bind:ready />
	{/if}
</div>
