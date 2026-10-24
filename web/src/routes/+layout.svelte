<script lang="ts">
	import '../app.css';
	import type { Snippet } from 'svelte';
	import { theme } from '$lib/theme.svelte';
	import Toast from '$lib/ui/Toast.svelte';

	let { children }: { children: Snippet } = $props();

	$effect(() => theme.follow());
</script>

{@render children()}
<Toast />
