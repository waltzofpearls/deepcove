package cli

import "strings"

// buildShellArgument quotes arg when a shell would split or change it, so a printed path pastes as one.
func buildShellArgument(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// buildShellCommand joins args into a line that runs as is when pasted into a shell.
func buildShellCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, buildShellArgument(arg))
	}
	return strings.Join(quoted, " ")
}
