import { goto } from '$app/navigation';
import { page } from '$app/state';
import { errorText, isCanceled, isUnauthenticated } from './api';

// SPEC §6.10.1: lists refresh every 10 s while the page is visible, never faster.
const refreshMs = 10_000;

export function toLogin(): void {
	if (page.url.pathname === '/login') return;
	const next = page.url.pathname + page.url.search;
	goto('/login?next=' + encodeURIComponent(next), { replaceState: true });
}

type State = { error: string; loading: boolean };

// watch runs fetch now and again whenever the reactive values fetch reads synchronously
// (route params, page tokens) change; with refresh also every 10 s while the tab is visible.
// Unauthenticated sends the user to /login. Must be called during component initialisation.
function watch<T>(s: State, fetch: (signal: AbortSignal) => Promise<T>, refresh: boolean, done: (v: T) => void) {
	$effect(() => {
		const ctl = new AbortController();
		const run = (p: Promise<T>) =>
			p.then(
				(v) => {
					done(v);
					s.error = '';
					s.loading = false;
				},
				(err) => {
					if (ctl.signal.aborted || isCanceled(err)) return;
					if (isUnauthenticated(err)) return toLogin();
					s.error = errorText(err);
					s.loading = false;
				}
			);
		run(fetch(ctl.signal));
		const timer = refresh
			? setInterval(() => {
					if (document.visibilityState === 'visible') run(fetch(ctl.signal));
				}, refreshMs)
			: undefined;
		return () => {
			ctl.abort();
			clearInterval(timer);
		};
	});
}

// Query holds the result of one RPC.
export class Query<T> {
	data = $state<T>();
	error = $state('');
	loading = $state(true);

	constructor(fetch: (signal: AbortSignal) => Promise<T>, opts: { refresh?: boolean } = {}) {
		watch(this, fetch, !!opts.refresh, (v) => (this.data = v));
	}

	// ready is set once the first answer (data or error) arrived; pages mark data-page then.
	get ready(): boolean {
		return !this.loading;
	}
}

// Paged holds one page of a token-paginated list RPC; prev walks back over seen tokens.
export class Paged<T> {
	items = $state<T[]>([]);
	next = $state('');
	error = $state('');
	loading = $state(true);
	#token = $state('');
	#seen = $state<string[]>([]);

	constructor(
		fetch: (pageToken: string, signal: AbortSignal) => Promise<{ items: T[]; next: string }>,
		opts: { refresh?: boolean } = {}
	) {
		watch(this, (signal) => fetch(this.#token, signal), !!opts.refresh, (v) => {
			this.items = v.items;
			this.next = v.next;
		});
	}

	get ready(): boolean {
		return !this.loading;
	}

	get hasPrev(): boolean {
		return this.#seen.length > 0;
	}

	forward(): void {
		this.#seen.push(this.#token);
		this.#token = this.next;
	}

	back(): void {
		this.#token = this.#seen.pop() ?? '';
	}
}
