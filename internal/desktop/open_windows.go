//go:build windows

// Package desktop provides native desktop integration.
package desktop

import (
	"context"
	"io"
	"os/exec"
)

// OpenURL opens target in the owner's default browser.
func OpenURL(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", target)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	return command.Run()
}
