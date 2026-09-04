//go:build !windows

package codexapp

import "fmt"

func codexConnectError(_ string, err error) error {
	return fmt.Errorf("connect to Codex app-server control socket: %w", err)
}
