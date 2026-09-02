//go:build !windows

package room

func agentShellExecutable(_ string, path string) string {
	return shellQuote(path)
}
