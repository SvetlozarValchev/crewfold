//go:build linux

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func probeLiveDatabase(path string) databaseFileProbe {
	var before unix.Stat_t
	if err := unix.Lstat(path, &before); err != nil {
		code := "database_unreadable"
		if errors.Is(err, os.ErrNotExist) {
			code = "database_missing"
		}
		return databaseFileProbe{Code: code, Detail: "live database path could not be inspected without following links: " + err.Error()}
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return databaseFileProbe{Code: "database_not_regular", Detail: "live database path is not a regular file"}
	}
	if before.Nlink != 1 {
		return databaseFileProbe{Code: "database_hardlinked", Detail: "live database must have exactly one filesystem link"}
	}
	if before.Uid != uint32(os.Geteuid()) {
		return databaseFileProbe{Code: "database_wrong_owner", Detail: "live database is not owned by the daemon effective user"}
	}
	if before.Mode&0o777 != 0o600 {
		return databaseFileProbe{Code: "database_unsafe_mode", Detail: "live database mode must be exactly 0600"}
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return databaseFileProbe{Code: "database_unreadable", Detail: "live database could not be opened without following links: " + err.Error()}
	}
	defer unix.Close(fd)
	var current unix.Stat_t
	if err := unix.Fstat(fd, &current); err != nil {
		return databaseFileProbe{Code: "database_unreadable", Detail: "opened live database could not be inspected: " + err.Error()}
	}
	if current.Dev != before.Dev || current.Ino != before.Ino {
		return databaseFileProbe{Code: "database_changed_during_probe", Detail: "live database path changed while it was being inspected"}
	}
	if current.Mode&unix.S_IFMT != unix.S_IFREG || current.Nlink != 1 || current.Uid != uint32(os.Geteuid()) || current.Mode&0o777 != 0o600 {
		return databaseFileProbe{Code: "database_changed_during_probe", Detail: "opened live database no longer has its exact private regular-file identity"}
	}
	if current.Size <= 0 {
		return databaseFileProbe{Code: "database_empty", Detail: "live database has no durable bytes"}
	}
	return databaseFileProbe{ByteSize: current.Size}
}
