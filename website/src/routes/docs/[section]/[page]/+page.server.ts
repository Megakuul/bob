import { error } from '@sveltejs/kit';
import { docs, find } from '$lib/server/docs';
import type { EntryGenerator, PageServerLoad } from './$types';

export const entries: EntryGenerator = () => docs.map((d) => ({ section: d.section, page: d.slug }));

export const load: PageServerLoad = ({ params }) => {
	const found = find(params.section, params.page);
	if (!found) error(404, 'No such page');
	return found;
};
