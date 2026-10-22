<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { client, errorText } from '$lib/api';
	import { toast } from '$lib/toast.svelte';
	import { ProjectService, type Project } from '$lib/gen/bob/project/v1/project_pb';
	import Button from '$lib/ui/Button.svelte';
	import Confirm from '$lib/ui/Confirm.svelte';
	import Input from '$lib/ui/Input.svelte';
	import KeyValue from '$lib/ui/KeyValue.svelte';
	import Section from '$lib/ui/Section.svelte';

	let { project }: { project: Project } = $props();

	const projects = client(ProjectService);
	// The form edits a copy; etag makes a concurrent change fail with Aborted instead of
	// overwriting it.
	const copy = (p: Project) => ({ ...p });
	let draft = $state(untrack(() => copy(project)));
	let deleting = $state(false);

	async function save(e: SubmitEvent) {
		e.preventDefault();
		try {
			const r = await projects.updateProject({
				project: $state.snapshot(draft),
				updateMask: { paths: ['display_name', 'description', 'repo_full_name'] }
			});
			if (r.project) draft = copy(r.project);
			toast.show('Saved.');
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}

	async function remove() {
		try {
			await projects.deleteProject({ id: project.id, etag: draft.etag });
			goto('/projects');
		} catch (err) {
			toast.show(errorText(err), true);
		}
	}
</script>

<div class="flex max-w-xl flex-col gap-6">
	<Section title="General">
		<form class="flex flex-col gap-3" onsubmit={save}>
			<Input label="Display name" bind:value={draft.displayName} />
			<Input label="Description" bind:value={draft.description} />
			<Input label="Repository (owner/name)" bind:value={draft.repoFullName} required />
			<div><Button variant="primary" type="submit">Save</Button></div>
		</form>
	</Section>
	<Section title="Repository link">
		<KeyValue
			rows={[
				{ k: 'Integration', v: project.forgeIntegrationId, mono: true },
				{ k: 'Repository id', v: String(project.repoId), mono: true },
				{ k: 'Default branch', v: project.defaultBranch, mono: true }
			]}
		/>
	</Section>
	<Section title="Delete project">
		<p class="text-muted">Deletes the project with its builds, logs and releases.</p>
		<div><Button variant="danger" onclick={() => (deleting = true)}>Delete project</Button></div>
	</Section>
</div>

<Confirm
	bind:open={deleting}
	title="Delete {project.id}?"
	text="This cannot be undone."
	confirm="Delete"
	danger
	onconfirm={remove}
/>
