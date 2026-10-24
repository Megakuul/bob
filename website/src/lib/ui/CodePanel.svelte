<script lang="ts">
	import { copyText } from '$lib/copy';

	// A file-like code panel; html is prerendered by src/lib/server/highlight.ts.
	let { file, code, html }: { file: string; code: string; html: string } = $props();
</script>

<div class="flex min-w-0 flex-col border border-line bg-surface" data-terminal>
	<div class="flex items-center justify-between border-b border-line py-1.5 pr-1.5 pl-3">
		<span class="font-mono text-sm text-muted">{file}</span>
		<button
			type="button"
			class="rounded-xs border border-line bg-bg px-2 py-0.5 font-mono text-sm text-fg hover:border-fg data-[done]:border-accent data-[done]:bg-accent data-[done]:text-on-accent"
			onclick={(e) => copyText(code, e.currentTarget)}
			aria-label="Copy {file}">copy</button
		>
	</div>
	<pre class="code-sample overflow-x-auto px-4 py-3 font-mono text-base leading-6 [tab-size:4]"><code>{@html html}</code></pre>
</div>

<style>
	.code-sample :global(.t-k) {
		font-weight: 700;
	}
	.code-sample :global(.t-s) {
		color: var(--color-ok);
	}
	.code-sample :global(.t-c) {
		color: var(--color-muted);
		font-style: italic;
	}
	.code-sample :global(.t-n) {
		color: var(--color-warn);
	}
</style>
