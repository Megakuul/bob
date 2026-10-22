// Display helpers for bob.build.v1.Build, shared by the overview, project and build pages.
import { BuildState, TriggerKind, type Build } from '$lib/gen/bob/build/v1/build_pb';
import { enumName, shortRef } from './format';

export function buildHref(b: Build): string {
	return `/p/${b.projectId}/builds/${b.id}`;
}

export function stateName(b: Build): string {
	return enumName(BuildState, b.state);
}

export function pipeline(b: Build): string {
	return b.pipelines.map((p) => p.name).join(', ') || (b.targets.length ? 'remote' : '');
}

export function ref(b: Build): string {
	return b.source?.kind.case === 'git' ? shortRef(b.source.kind.value.ref) : 'upload';
}

export function sha(b: Build): string {
	return b.source?.kind.case === 'git' ? b.source.kind.value.sha.slice(0, 7) : '';
}

export function trigger(b: Build): string {
	const kind = enumName(TriggerKind, b.trigger?.kind ?? 0).toLowerCase().replace('_', ' ');
	const who = b.trigger?.sender || b.trigger?.principal?.displayName || '';
	return [kind, who].filter(Boolean).join(' ');
}

export function progress(b: Build): string {
	const c = b.counts;
	return c && c.total ? `${c.cached + c.built}/${c.total}` : '';
}

export function errorCode(b: Build): string {
	return b.errors[0]?.code ?? '';
}

export function running(b: Build): boolean {
	return b.state === BuildState.PENDING || b.state === BuildState.QUEUED || b.state === BuildState.RUNNING;
}
