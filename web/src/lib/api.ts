// The one connect transport of the UI. Everything goes to the public API under /api/ (SPEC
// §6.2, R-API-004) as Connect JSON over POST: cookie-only Connect GET is refused by the server
// and every cookie-authenticated request must carry Bob-Csrf (§6.3.2).
import { Code, ConnectError, createClient, type Client, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import type { DescService } from '@bufbuild/protobuf';

const csrf: Interceptor = (next) => (req) => {
	req.header.set('Bob-Csrf', '1');
	return next(req);
};

const transport = createConnectTransport({ baseUrl: '/api', interceptors: [csrf] });

export function client<T extends DescService>(service: T): Client<T> {
	return createClient(service, transport);
}

export function isUnauthenticated(err: unknown): boolean {
	return ConnectError.from(err).code === Code.Unauthenticated;
}

export function isCanceled(err: unknown): boolean {
	return ConnectError.from(err).code === Code.Canceled;
}

// errorText is what a page shows for a failed call: the server's message, never a stack.
export function errorText(err: unknown): string {
	return ConnectError.from(err).rawMessage || 'request failed';
}
