<script lang="ts">
	import { copyText } from '$lib/copy';
	import Backdrop from './Backdrop.svelte';

	// A terminal-style snippet. Lines starting with "$ " are commands (the copy button copies
	// only those, without the prompt); "# " lines are comments.
	let { title = 'shell', lines }: { title?: string; lines: string[] } = $props();

	const commands = $derived(lines.filter((l) => l.startsWith('$ ')).map((l) => l.slice(2)));
</script>

<div class="relative border border-line bg-surface" data-terminal>
	<div class="flex items-center justify-between border-b border-line py-1.5 pr-1.5 pl-3">
		<span class="flex items-center gap-2 font-mono text-sm text-muted">
			<i class="inline-block h-2 w-2 bg-accent" aria-hidden="true"></i>{title}
		</span>
		<button
			type="button"
			class="rounded-xs border border-line bg-bg px-2 py-0.5 font-mono text-sm text-fg hover:border-fg data-[done]:border-accent data-[done]:bg-accent data-[done]:text-on-accent"
			onclick={(e) => copyText(commands.join('\n'), e.currentTarget)}
			aria-label="Copy commands">copy</button
		>
	</div>
	<div class="relative overflow-x-auto">
		<Backdrop kind="scan" />
		<ol class="relative px-3 py-3 font-mono text-base leading-7 sm:px-4">
			{#each lines as l, i (i)}
				<li class="whitespace-pre">
					{#if l.startsWith('$ ')}<span class="text-muted select-none" aria-hidden="true">$ </span><span class="text-fg">{l.slice(2)}</span>{:else if l.startsWith('# ')}<span class="text-muted">{l}</span>{:else}<span class="text-fg">{l}</span>{/if}
				</li>
			{/each}
		</ol>
	</div>
</div>
