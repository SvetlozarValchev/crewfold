//go:build !windows

package room

func hostedAgentExecutable(path string) string {
	return shellQuote(path)
}
