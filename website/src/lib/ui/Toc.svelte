<script lang="ts">
	// "On this page": the h2/h3 headings, with the one in view marked.
	let { headings }: { headings: { depth: number; text: string; id: string }[] } = $props();

	let active = $state('');

	$effect(() => {
		const els = headings.map((h) => document.getElementById(h.id)).filter((e): e is HTMLElement => e !== null);
		if (els.length === 0) return;
		const seen = new Set<string>();
		const io = new IntersectionObserver(
			(entries) => {
				for (const e of entries) e.isIntersecting ? seen.add(e.target.id) : seen.delete(e.target.id);
				const first = headings.find((h) => seen.has(h.id));
				if (first) active = first.id;
			},
			{ rootMargin: '-64px 0px -60% 0px' }
		);
		els.forEach((e) => io.observe(e));
		return () => io.disconnect();
	});
</script>

{#if headings.length > 0}
	<nav aria-label="On this page" class="flex flex-col gap-2">
		<p class="font-mono text-xs tracking-wider text-muted uppercase">On this page</p>
		<ul class="flex flex-col border-l border-line text-sm">
			{#each headings as h (h.id)}
				<li>
					<a
						href="#{h.id}"
						class="-ml-px block border-l-2 py-1 {h.depth === 3 ? 'pl-6' : 'pl-3'} {active === h.id ? 'border-accent text-fg' : 'border-transparent text-muted hover:text-fg'}"
						>{h.text}</a
					>
				</li>
			{/each}
		</ul>
	</nav>
{/if}
