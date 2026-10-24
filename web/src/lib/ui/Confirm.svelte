<script lang="ts">
	import Button from './Button.svelte';
	import Dialog from './Dialog.svelte';

	let { open = $bindable(false), title, text, confirm = 'Confirm', danger = false, onconfirm }: {
		open?: boolean;
		title: string;
		text: string;
		confirm?: string;
		danger?: boolean;
		onconfirm: () => void;
	} = $props();
</script>

<Dialog bind:open {title}>
	<p>{text}</p>
	{#snippet actions()}
		<Button onclick={() => (open = false)}>Cancel</Button>
		<Button
			variant={danger ? 'danger' : 'primary'}
			onclick={() => {
				open = false;
				onconfirm();
			}}>{confirm}</Button
		>
	{/snippet}
</Dialog>
