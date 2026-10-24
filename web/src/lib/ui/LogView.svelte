<script lang="ts">
	// Virtualised log list (SPEC §6.10.1): only the visible window of fixed-height lines is in
	// the DOM. Text goes in as text nodes; ANSI colours map to tokens.
	import { parse } from '$lib/ansi';
	import type { Line } from '$lib/log.svelte';

	let { lines, follow = true, showPackage = false }: { lines: Line[]; follow?: boolean; showPackage?: boolean } =
		$props();

	const lineHeight = 18;
	const overscan = 20;
	let viewport: HTMLDivElement;
	let scrollTop = $state(0);
	let height = $state(480);

	const first = $derived(Math.max(0, Math.floor(scrollTop / lineHeight) - overscan));
	const last = $derived(Math.min(lines.length, Math.ceil((scrollTop + height) / lineHeight) + overscan));
	const visible = $derived(lines.slice(first, last));

	const look = { '': '', fail: 'text-fail', ok: 'text-ok', warn: 'text-warn', muted: 'text-muted' };

	$effect(() => {
		void lines.length;
		if (follow && viewport) {
			viewport.scrollTop = viewport.scrollHeight;
			scrollTop = viewport.scrollTop;
		}
	});
</script>

<div
	bind:this={viewport}
	bind:clientHeight={height}
	onscroll={() => (scrollTop = viewport.scrollTop)}
	class="h-[480px] overflow-auto bg-surface font-mono text-sm leading-[18px]"
>
	<div class="relative" style="height: {lines.length * lineHeight}px">
		<div class="absolute inset-x-0" style="top: {first * lineHeight}px">
			{#each visible as l (l.key)}
				<div data-line class="h-[18px] px-2 whitespace-pre">
					{#if showPackage}<span class="mr-2 text-muted select-none">{l.package}</span>{/if}{#each parse(l.text) as s, i (i)}<span
							class={look[s.tone]}
							class:font-semibold={s.bold}>{s.text}</span
						>{/each}
				</div>
			{/each}
		</div>
	</div>
</div>
