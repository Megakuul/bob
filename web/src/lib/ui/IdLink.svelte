<script lang="ts">
	// Every id renders as a link to its detail route (R-UI-006); title carries the full id.
	let { id, href, text }: { id: string; href: string; text?: string } = $props();
</script>

<a class="id font-mono text-sm hover:underline" {href} title={id}>{text ?? id}</a>
