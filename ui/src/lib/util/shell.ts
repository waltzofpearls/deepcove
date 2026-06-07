// shellQuote quotes a string for the copy-pasteable CLI command shown in the New
// Session dialog. Strings made only of shell-safe characters pass through bare;
// anything else is single-quoted with embedded single quotes escaped ('\'').
export function shellQuote(s: string): string {
	if (/^[A-Za-z0-9_@%+=:,./\-~]+$/.test(s)) return s;
	return `'${s.replace(/'/g, "'\\''")}'`;
}
