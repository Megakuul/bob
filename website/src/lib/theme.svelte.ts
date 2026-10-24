// Theme toggle. app.html picks the theme before first paint (stored choice, else the OS
// preference, else dark); this module flips it and stores the choice in localStorage.
const key = 'bob.site.theme';

export type Theme = 'dark' | 'light';

class ThemeState {
	current = $state<Theme>('dark');

	/** Reads what app.html applied; call once on mount. */
	sync(): void {
		this.current = document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark';
	}

	toggle(): void {
		this.current = this.current === 'dark' ? 'light' : 'dark';
		document.documentElement.setAttribute('data-theme', this.current);
		try {
			localStorage.setItem(key, this.current);
		} catch {
			// storage blocked: the choice holds for this page only
		}
	}
}

export const theme = new ThemeState();
