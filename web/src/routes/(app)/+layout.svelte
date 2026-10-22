<script lang="ts">
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { client } from '$lib/api';
	import { Query } from '$lib/query.svelte';
	import { AuthService } from '$lib/gen/bob/auth/v1/auth_pb';
	import ThemeSwitch from '$lib/ui/ThemeSwitch.svelte';

	let { children }: { children: Snippet } = $props();

	const auth = client(AuthService);
	// WhoAmI doubles as the session check: Unauthenticated sends the user to /login.
	const me = new Query((signal) => auth.whoAmI({}, { signal }));

	const items = [
		{ href: '/', label: 'Overview' },
		{ href: '/projects', label: 'Projects' },
		{ href: '/fleet', label: 'Fleet' },
		{ href: '/cache', label: 'Cache' },
		{ href: '/registry', label: 'Registry' },
		{ href: '/access', label: 'Access' },
		{ href: '/settings', label: 'Settings' },
		{ href: '/analytics', label: 'Analytics' }
	];

	function active(href: string, path: string): boolean {
		if (href === '/') return path === '/';
		if (href === '/projects') return path === '/projects' || path.startsWith('/p/');
		return path === href || path.startsWith(href + '/');
	}
</script>

<div class="flex min-h-screen flex-col md:block md:pl-[200px]">
	<aside
		class="flex items-center border-b border-line bg-surface md:fixed md:inset-y-0 md:left-0 md:w-[200px] md:flex-col md:items-stretch md:border-r md:border-b-0"
	>
		<a href="/" class="px-4 py-2.5 text-lg font-semibold md:py-3">bob</a>
		<nav class="flex min-w-0 flex-1 overflow-x-auto md:flex-none md:flex-col md:overflow-visible">
			{#each items as it (it.href)}
				<a
					href={it.href}
					aria-current={active(it.href, page.url.pathname) ? 'page' : undefined}
					class="border-b-2 px-3 py-2.5 whitespace-nowrap md:border-b-0 md:border-l-2 md:px-4 md:py-1.5 {active(
						it.href,
						page.url.pathname
					)
						? 'border-accent font-medium text-fg'
						: 'border-transparent text-muted hover:text-fg'}">{it.label}</a
				>
			{/each}
		</nav>
		<div class="hidden flex-col gap-2 border-t border-line px-4 py-3 md:mt-auto md:flex">
			{#if me.data}
				<a href="/account" class="truncate text-muted hover:text-fg">{me.data.login}</a>
			{/if}
			<ThemeSwitch />
		</div>
	</aside>
	<main class="flex flex-1 flex-col gap-5 px-4 py-4 md:px-6">
		{@render children()}
	</main>
	<footer class="flex items-center justify-between gap-2 border-t border-line px-4 py-3 md:hidden">
		{#if me.data}
			<a href="/account" class="truncate text-muted">{me.data.login}</a>
		{/if}
		<ThemeSwitch />
	</footer>
</div>
