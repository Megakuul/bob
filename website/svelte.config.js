import adapter from '@sveltejs/adapter-static';
import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

/** sha256 over the sources (paths and contents, sorted): the site version without a clock. */
function sourceHash(roots) {
	const h = createHash('sha256');
	const walk = (p) => {
		if (statSync(p).isDirectory()) {
			for (const name of readdirSync(p).sort()) walk(join(p, name));
		} else {
			h.update(p + '\0').update(readFileSync(p)).update('\0');
		}
	};
	for (const r of roots) if (existsSync(r)) walk(r);
	return h.digest('hex').slice(0, 16);
}

/** @type {import('@sveltejs/kit').Config} */
export default {
	kit: {
		// Every page is prerendered; 404.html is the shell static hosts serve for unknown paths.
		adapter: adapter({ pages: 'build', assets: 'build', fallback: '404.html', strict: true }),
		prerender: { handleHttpError: 'fail', handleMissingId: 'warn' },
		// SvelteKit's default version is Date.now(), which makes every build differ.
		version: {
			name: sourceHash(['src', 'content', 'static', 'package.json', 'svelte.config.js', 'vite.config.ts'])
		}
	}
};
