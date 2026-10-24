import adapter from '@sveltejs/adapter-static';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { join } from 'node:path';

// Static build with an SPA fallback (R-UI-001).
const site = adapter({ pages: 'build', assets: 'build', fallback: '200.html' });

// internal/ui embeds this directory (go:embed cannot reach web/build). Its committed .gitignore
// marks a bob checkout; a build elsewhere (bob's sandbox) has no such directory and skips it.
const embed = '../internal/ui/dist';

/** Replaces the contents of embed (except its .gitignore) with build/. */
function mirror() {
	if (!existsSync(join(embed, '.gitignore'))) return;
	for (const name of readdirSync(embed)) {
		if (name !== '.gitignore') rmSync(join(embed, name), { recursive: true, force: true });
	}
	cpSync('build', embed, { recursive: true });
}

/** sha256 over the sources (paths and contents, sorted): the app version without a clock. */
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
		adapter: {
			name: site.name,
			async adapt(builder) {
				await site.adapt(builder);
				mirror();
			}
		},
		// SvelteKit's default version is Date.now(), which makes every build differ.
		version: { name: sourceHash(['src', 'package.json', 'svelte.config.js', 'vite.config.ts']) }
	}
};
