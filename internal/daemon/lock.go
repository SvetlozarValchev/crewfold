package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type dataDirLock struct {
	file *os.File
}

func acquireDataDirLock(dataDir string) (*dataDirLock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, &StartupError{
			Code:    CodeInvalidConfiguration,
			Message: fmt.Sprintf("create data directory %s", dataDir),
			Cause:   err,
		}
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "inspect data directory", Cause: err}
	}
	if !info.IsDir() {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: fmt.Sprintf("data directory path is not a directory: %s", dataDir)}
	}

	lockPath := filepath.Join(dataDir, "daemon.lock")
	lockInfo, err := os.Lstat(lockPath)
	if err == nil && !lockInfo.Mode().IsRegular() {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: fmt.Sprintf("data directory lock is not a regular file: %s", lockPath)}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "inspect data directory lock", Cause: err}
	}

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "open data directory lock", Cause: err}
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "set data directory lock permissions", Cause: err}
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &StartupError{
				Code:    CodeDataDirInUse,
				Message: fmt.Sprintf("data directory is already owned by a live Crewfold daemon: %s", dataDir),
				Cause:   err,
			}
		}
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "lock data directory", Cause: err}
	}

	if err := file.Truncate(0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "truncate data directory lock", Cause: err}
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "seek data directory lock", Cause: err}
	}
	if _, err := file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, &StartupError{Code: CodeInvalidConfiguration, Message: "write data directory lock", Cause: err}
	}

	return &dataDirLock{file: file}, nil
}

func (lock *dataDirLock) release() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	_ = lock.file.Close()
}
