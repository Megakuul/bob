// One transient message at a time, shown by ui/Toast.svelte in the app shell.
class Toast {
	text = $state('');
	error = $state(false);
	#timer: ReturnType<typeof setTimeout> | undefined;

	show(text: string, error = false): void {
		this.text = text;
		this.error = error;
		clearTimeout(this.#timer);
		this.#timer = setTimeout(() => (this.text = ''), 4000);
	}
}

export const toast = new Toast();
