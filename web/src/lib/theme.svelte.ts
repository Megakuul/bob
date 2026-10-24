// Theme choice (SPEC §6.10.1, R-UI-004): Light by default regardless of the OS, Dark or System
// on request, stored in localStorage["bob.theme"] and applied as data-theme on <html>. app.html
// applies the stored choice before first paint; this module keeps it in sync afterwards.
export type Choice = 'light' | 'dark' | 'system';

const key = 'bob.theme';

function stored(): Choice {
	try {
		const v = localStorage.getItem(key);
		return v === 'dark' || v === 'system' ? v : 'light';
	} catch {
		return 'light';
	}
}

function apply(c: Choice): void {
	const dark = c === 'dark' || (c === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
	document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
}

class Theme {
	choice = $state<Choice>(stored());

	set(c: Choice): void {
		this.choice = c;
		try {
			localStorage.setItem(key, c);
		} catch {
			// storage blocked: the choice holds for this page only
		}
		apply(c);
	}

	// follow re-applies System when the OS preference changes; returns the unsubscribe.
	follow(): () => void {
		const mq = matchMedia('(prefers-color-scheme: dark)');
		const on = () => this.choice === 'system' && apply('system');
		mq.addEventListener('change', on);
		return () => mq.removeEventListener('change', on);
	}
}

export const theme = new Theme();
