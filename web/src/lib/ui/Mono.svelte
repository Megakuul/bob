<script lang="ts">
	let { text, copy = false }: { text: string; copy?: boolean } = $props();
	let copied = $state(false);

	async function write() {
		try {
			await navigator.clipboard.writeText(text);
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			// clipboard denied: the text is selectable anyway
		}
	}
</script>

<span class="inline-flex max-w-full items-center gap-2">
	<span class="truncate font-mono text-sm">{text}</span>
	{#if copy}
		<button type="button" onclick={write} class="text-sm text-muted hover:text-fg hover:underline">
			{copied ? 'copied' : 'copy'}
		</button>
	{/if}
</span>
