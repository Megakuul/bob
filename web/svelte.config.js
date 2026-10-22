import adapter from '@sveltejs/adapter-static';

/** @type {import('@sveltejs/kit').Config} */
export default {
	kit: {
		// Static build with an SPA fallback, embedded into bob-server by internal/ui (R-UI-001).
		adapter: adapter({ pages: 'build', assets: 'build', fallback: '200.html' })
	}
};
