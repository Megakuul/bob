// Clipboard copy with a short "copied" state on the button.
export async function copyText(text: string, button: HTMLElement): Promise<void> {
	try {
		await navigator.clipboard.writeText(text);
	} catch {
		// clipboard blocked (insecure context): select the text instead so the user can copy it
		const pre = button.closest('.code, [data-terminal]')?.querySelector('pre, ol');
		if (pre) getSelection()?.selectAllChildren(pre);
		return;
	}
	button.setAttribute('data-done', '');
	const label = button.textContent;
	button.textContent = 'copied';
	setTimeout(() => {
		button.removeAttribute('data-done');
		button.textContent = label;
	}, 1400);
}

/** Shell snippets with "$ " prompts copy only the commands, without prompts or output. */
export function shellCommands(code: string): string {
	const lines = code.split('\n');
	if (!lines.some((l) => l.startsWith('$ '))) return code;
	return lines
		.filter((l) => l.startsWith('$ '))
		.map((l) => l.slice(2))
		.join('\n');
}
