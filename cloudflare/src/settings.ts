// Pure checks on the Worker's settings; no Cloudflare imports, so a plain
// node script can run them.

// A value that is unset, empty or only whitespace counts as unset.
export function isSet(value: string | undefined): value is string {
	return value !== undefined && value.trim() !== "";
}

// The form @cloudflare/containers accepts, above zero.
const sleepAfterForm = /^[1-9][0-9]*[smh]$/;

// The sleep time to give the container library: undefined keeps its
// default; null means the value is invalid and must never reach it, since
// the library throws on a time it cannot read.
export function sleepAfter(value: string | undefined): string | undefined | null {
	if (!isSet(value)) {
		return undefined;
	}
	return sleepAfterForm.test(value) ? value : null;
}

// The optional settings to pass to wenna: each one that is set, unchanged.
// wenna validates them and refuses a set-but-blank value, so a blank one is
// never passed.
export function optionalVars<K extends string>(
	names: readonly K[],
	env: Partial<Record<K, string>>,
): Partial<Record<K, string>> {
	const vars: Partial<Record<K, string>> = {};
	for (const name of names) {
		const value = env[name];
		if (isSet(value)) {
			vars[name] = value;
		}
	}
	return vars;
}
