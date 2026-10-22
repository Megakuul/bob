// Worker actions shared by the fleet list and the worker page.
import { client, errorText } from './api';
import { toast } from './toast.svelte';
import { FleetService, WorkerState, type Worker } from '$lib/gen/bob/fleet/v1/fleet_pb';

const fleet = client(FleetService);

export async function workerAction(kind: 'drain' | 'undrain' | 'revoke', id: string): Promise<Worker | undefined> {
	try {
		const r =
			kind === 'drain'
				? await fleet.drainWorker({ id })
				: kind === 'undrain'
					? await fleet.undrainWorker({ id })
					: await fleet.revokeWorker({ id, reason: 'revoked in the UI' });
		return r.worker;
	} catch (err) {
		toast.show(errorText(err), true);
	}
}

export function labels(w: Worker): string {
	return Object.entries(w.labels)
		.map(([k, v]) => (v ? `${k}=${v}` : k))
		.join(' ');
}

export const draining = (w: Worker) => w.state === WorkerState.DRAINING;
export const revoked = (w: Worker) => w.state === WorkerState.REVOKED;
