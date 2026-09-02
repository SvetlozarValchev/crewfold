//go:build windows

package room

import "strings"

func hostedAgentExecutable(path string) string {
	// Native Codex uses PowerShell on Windows. The call operator is required
	// when a quoted executable path contains spaces.
	return "& " + powershellQuote(path)
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
