//go:build !linux

package daemon

import (
	"errors"
	"os"
)

func probeLiveDatabase(path string) databaseFileProbe {
	before, err := os.Lstat(path)
	if err != nil {
		code := "database_unreadable"
		if errors.Is(err, os.ErrNotExist) {
			code = "database_missing"
		}
		return databaseFileProbe{Code: code, Detail: "live database path could not be inspected without following links: " + err.Error()}
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return databaseFileProbe{Code: "database_not_regular", Detail: "live database path is not a regular file"}
	}
	if before.Mode().Perm() != 0o600 {
		return databaseFileProbe{Code: "database_unsafe_mode", Detail: "live database mode must be exactly 0600"}
	}
	file, err := os.Open(path)
	if err != nil {
		return databaseFileProbe{Code: "database_unreadable", Detail: "live database could not be opened: " + err.Error()}
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !os.SameFile(before, current) {
		return databaseFileProbe{Code: "database_changed_during_probe", Detail: "live database path changed while it was being inspected"}
	}
	if current.Size() <= 0 {
		return databaseFileProbe{Code: "database_empty", Detail: "live database has no durable bytes"}
	}
	// Exact owner and link-count verification is a Linux personal-beta
	// guarantee. Other targets fail closed instead of claiming that proof.
	return databaseFileProbe{Code: "database_identity_unverified", Detail: "exact database ownership and link count are not verifiable on this platform"}
}
