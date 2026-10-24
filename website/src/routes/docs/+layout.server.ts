import { searchIndex, sections } from '$lib/server/docs';
import type { LayoutServerLoad } from './$types';

// Navigation and the search index are computed at prerender time and shipped as page data.
export const load: LayoutServerLoad = () => ({ sections, search: searchIndex });
