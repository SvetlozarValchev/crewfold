package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	dataDirectoryMode = 0o700
	dataLockMode      = 0o600
)

type dataDirLock struct {
	file      *os.File
	directory *os.File
	created   bool
	committed bool
}

func acquireDataDirLock(dataDir string) (*dataDirLock, error) {
	directory, err := openOrCreateDataDirectoryNoFollow(dataDir)
	if err != nil {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "open private data directory without following links", Cause: err}
	}
	fail := func(message string, cause error) (*dataDirLock, error) {
		_ = directory.Close()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: message, Cause: cause}
	}

	lockPath := filepath.Join(dataDir, "daemon.lock")
	lockFD, err := unix.Openat(int(directory.Fd()), "daemon.lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, dataLockMode)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		lockFD, err = unix.Openat(int(directory.Fd()), "daemon.lock", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	}
	if err != nil {
		return fail("open data directory lock", err)
	}
	file := os.NewFile(uintptr(lockFD), lockPath)
	closeBoth := func() {
		_ = file.Close()
		_ = directory.Close()
	}
	if created {
		if err := unix.Fchmod(lockFD, dataLockMode); err != nil {
			closeBoth()
			return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "set new data directory lock permissions", Cause: err}
		}
		if err := directory.Sync(); err != nil {
			closeBoth()
			return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "sync new data directory lock", Cause: err}
		}
	}
	if err := validatePrivateDataLock(lockFD); err != nil {
		closeBoth()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "data directory lock is not exact private single-link storage", Cause: err}
	}

	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeBoth()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, &StartupError{
				Code:    CodeDataDirInUse,
				Message: fmt.Sprintf("data directory is already owned by a live Crewfold daemon: %s", dataDir),
				Cause:   err,
			}
		}
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "lock data directory", Cause: err}
	}
	unlockAndClose := func() {
		_ = unix.Flock(lockFD, unix.LOCK_UN)
		closeBoth()
	}
	// Revalidate after acquiring flock and before the first mutation. An unsafe
	// existing hard link or permission change is never normalized in place.
	if err := validatePrivateDataLock(lockFD); err != nil {
		unlockAndClose()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "locked data directory lock is not exact private single-link storage", Cause: err}
	}
	return &dataDirLock{file: file, directory: directory, created: created}, nil
}

// recordOwnerPID publishes operational ownership only after startup recovery
// has verified or consumed every restore seal. Acquiring the flock itself is
// intentionally content-preserving so an unsafe restore is rejected without
// changing any selected target byte.
func (lock *dataDirLock) recordOwnerPID() error {
	if lock == nil || lock.file == nil {
		return &StartupError{Code: CodeInvalidConfiguration, Message: "record data directory owner", Cause: errors.New("data directory lock is not open")}
	}
	lockFD := int(lock.file.Fd())
	if err := validatePrivateDataLock(lockFD); err != nil {
		return &StartupError{Code: CodeInvalidConfiguration, Message: "locked data directory lock is not exact private single-link storage", Cause: err}
	}
	if err := lock.file.Truncate(0); err != nil {
		return &StartupError{Code: CodeInvalidConfiguration, Message: "truncate data directory lock", Cause: err}
	}
	if _, err := lock.file.Seek(0, 0); err != nil {
		return &StartupError{Code: CodeInvalidConfiguration, Message: "seek data directory lock", Cause: err}
	}
	if _, err := lock.file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		return &StartupError{Code: CodeInvalidConfiguration, Message: "write data directory lock", Cause: err}
	}
	if err := lock.file.Sync(); err != nil {
		return &StartupError{Code: CodeInvalidConfiguration, Message: "sync data directory lock", Cause: err}
	}
	lock.committed = true
	return nil
}

func openOrCreateDataDirectoryNoFollow(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, errors.New("data directory must be a non-root canonical absolute path")
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(current)
			return nil, errors.New("data directory contains a noncanonical component")
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		created := false
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(current, component, dataDirectoryMode); mkdirErr != nil {
				_ = unix.Close(current)
				return nil, mkdirErr
			}
			created = true
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if openErr == nil {
				if chmodErr := unix.Fchmod(next, dataDirectoryMode); chmodErr != nil {
					_ = unix.Close(next)
					_ = unix.Close(current)
					return nil, chmodErr
				}
				if syncErr := unix.Fsync(current); syncErr != nil {
					_ = unix.Close(next)
					_ = unix.Close(current)
					return nil, syncErr
				}
			}
		}
		_ = unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		current = next
		if index == len(components)-1 {
			var stat unix.Stat_t
			if err := unix.Fstat(current, &stat); err != nil {
				_ = unix.Close(current)
				return nil, err
			}
			if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != dataDirectoryMode {
				_ = unix.Close(current)
				if created {
					return nil, errors.New("new data directory is not exact owner-controlled mode 0700")
				}
				return nil, errors.New("existing data directory is not exact owner-controlled mode 0700")
			}
		}
	}
	return os.NewFile(uintptr(current), path), nil
}

func validatePrivateDataLock(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != dataLockMode {
		return errors.New("lock must be an owner-held 0600 single-link regular file")
	}
	return nil
}

func (lock *dataDirLock) release() {
	if lock == nil {
		return
	}
	if lock.created && !lock.committed {
		lock.removeUncommittedCreatedFile()
	}
	if lock.file != nil {
		_ = unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
		_ = lock.file.Close()
	}
	if lock.directory != nil {
		_ = lock.directory.Close()
	}
}

func (lock *dataDirLock) removeUncommittedCreatedFile() {
	if lock.file == nil || lock.directory == nil {
		return
	}
	var openStat unix.Stat_t
	if err := unix.Fstat(int(lock.file.Fd()), &openStat); err != nil {
		return
	}
	var namedStat unix.Stat_t
	if err := unix.Fstatat(int(lock.directory.Fd()), "daemon.lock", &namedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return
	}
	if openStat.Dev != namedStat.Dev || openStat.Ino != namedStat.Ino {
		return
	}
	if err := validatePrivateDataLock(int(lock.file.Fd())); err != nil {
		return
	}
	if err := unix.Unlinkat(int(lock.directory.Fd()), "daemon.lock", 0); err != nil {
		return
	}
	_ = lock.directory.Sync()
}
