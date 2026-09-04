//go:build !windows

package room

func needsCodexPasteRecovery() bool { return false }
