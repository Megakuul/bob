// LogFeed is the log of one build (all packages): the persisted part from GetLog, then live
// lines from FollowLog until the server says done (SPEC §6.10.1, R-LOG).
import { Code, ConnectError } from '@connectrpc/connect';
import { client, isUnauthenticated } from './api';
import { toLogin } from './query.svelte';
import { LogService, type LogLine } from '$lib/gen/bob/log/v1/log_pb';

export type Line = { key: string; package: string; seq: bigint; attempt: number; text: string };

const logs = client(LogService);
const retryMs = 2_000;

function line(l: LogLine): Line {
	return { key: `${l.package}#${l.attempt}#${l.seq}`, package: l.package, seq: l.seq, attempt: l.attempt, text: l.text };
}

export class LogFeed {
	lines = $state.raw<Line[]>([]);
	truncated = $state(false);
	delayed = $state(false);
	done = $state(false);
	error = $state('');
	#cursor: Record<string, { seq: bigint; attempt: number }> = {};

	// buildId is read once per effect run; the feed restarts when it changes.
	constructor(buildId: () => string) {
		$effect(() => {
			const id = buildId();
			const ctl = new AbortController();
			this.lines = [];
			this.#cursor = {};
			this.done = false;
			this.#run(id, ctl.signal);
			return () => ctl.abort();
		});
	}

	#add(ls: LogLine[]): void {
		const fresh: Line[] = [];
		for (const l of ls) {
			const c = this.#cursor[l.package];
			if (c && (l.attempt < c.attempt || (l.attempt === c.attempt && l.seq <= c.seq))) continue;
			this.#cursor[l.package] = { seq: l.seq, attempt: l.attempt };
			fresh.push(line(l));
		}
		if (fresh.length) this.lines = this.lines.concat(fresh);
	}

	async #run(buildId: string, signal: AbortSignal): Promise<void> {
		try {
			const r = await logs.getLog({ buildId }, { signal });
			this.truncated = r.truncated;
			this.#add(r.lines);
		} catch (err) {
			if (signal.aborted) return;
			if (isUnauthenticated(err)) return toLogin();
			this.error = ConnectError.from(err).rawMessage;
		}
		while (!signal.aborted && !this.done) {
			try {
				for await (const m of logs.followLog({ buildId, cursor: this.#cursor }, { signal })) {
					for (const r of m.restarts) {
						this.lines = this.lines.filter((l) => l.package !== r.package || l.seq < r.fromSeq);
						this.#cursor[r.package] = { seq: r.fromSeq - 1n, attempt: r.attempt };
					}
					this.#add(m.lines);
					this.delayed = m.delayed;
					if (m.done) this.done = true;
				}
				if (this.done) return;
			} catch (err) {
				if (signal.aborted) return;
				const code = ConnectError.from(err).code;
				if (code === Code.Unauthenticated) return toLogin();
				if (code === Code.Unimplemented || code === Code.NotFound || code === Code.PermissionDenied) return;
			}
			await new Promise((r) => setTimeout(r, retryMs));
		}
	}
}
