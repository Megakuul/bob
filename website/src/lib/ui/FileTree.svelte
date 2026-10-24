<script lang="ts">
	import { copyText } from '$lib/copy';

	// A ```tree listing outside the docs; html and text come from src/lib/server/tree.ts.
	let { title, html, text }: { title: string; html: string; text: string } = $props();
</script>

<div class="flex min-w-0 flex-col border border-line bg-surface">
	<div class="flex items-center justify-between border-b border-line py-1.5 pr-1.5 pl-3">
		<span class="font-mono text-sm text-muted">{title}</span>
		<button
			type="button"
			class="rounded-xs border border-line bg-bg px-2 py-0.5 font-mono text-sm text-fg hover:border-fg data-[done]:border-accent data-[done]:bg-accent data-[done]:text-on-accent"
			onclick={(e) => copyText(text, e.currentTarget)}
			aria-label="Copy file tree">copy</button
		>
	</div>
	<div class="tree-body" role="group" aria-label="File tree">{@html html}</div>
</div>
