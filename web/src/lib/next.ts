// safeNext returns the ?next= target when it is a path on this server, else "/": a login
// page must never redirect to another origin.
export function safeNext(next: string | null): string {
	return next && next.startsWith('/') && !next.startsWith('//') && !next.startsWith('/\\') ? next : '/';
}
