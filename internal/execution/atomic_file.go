package execution

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const atomicPrivateFileMaximumBytes = int64(16 * 1024 * 1024)

var errAtomicPrivateFileExists = errors.New("private atomic file already exists")

type atomicPrivateFilePoint string

const (
	atomicPrivateFileBeforeName atomicPrivateFilePoint = "before_name"
	atomicPrivateFileAfterStage atomicPrivateFilePoint = "after_stage_name"
	atomicPrivateFileAfterName  atomicPrivateFilePoint = "after_final_name"
)

type atomicPrivateFileHook func(atomicPrivateFilePoint) error

// publishPrivateFileNoReplace writes through an anonymous inode and gives it
// exactly one name only after its bytes are durable. A process loss before the
// link leaves no directory entry; a process loss after it leaves the complete
// single-link target for exact replay.
func publishPrivateFileNoReplace(directoryPath, name string, data []byte, hook atomicPrivateFileHook) error {
	directoryFD, err := openLockedAtomicDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer closeLockedAtomicDirectory(directoryFD)
	if err := validateAtomicPrivateFileName(name); err != nil {
		return err
	}
	temporary, err := createAnonymousPrivateFile(directoryFD, data)
	if err != nil {
		return err
	}
	defer temporary.Close()
	if hook != nil {
		if err := hook(atomicPrivateFileBeforeName); err != nil {
			return err
		}
	}
	if err := unix.Linkat(int(temporary.Fd()), "", directoryFD, name, unix.AT_EMPTY_PATH); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errAtomicPrivateFileExists
		}
		return fmt.Errorf("publish private atomic file: %w", err)
	}
	if hook != nil {
		if err := hook(atomicPrivateFileAfterName); err != nil {
			return err
		}
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return fmt.Errorf("sync private atomic file directory: %w", err)
	}
	return verifyAtomicPrivateFileAt(directoryFD, name, data)
}

// replacePrivateFileAtomic uses one deterministic, reserved stage name. The
// stage is linked only after its anonymous inode is complete and durable. A
// reader or later writer promotes a stage left by process loss before reading
// the target, so no random temporary names accumulate and no complete update
// is silently discarded.
func replacePrivateFileAtomic(path string, data []byte, hook atomicPrivateFileHook) error {
	directoryPath, name := filepath.Dir(path), filepath.Base(path)
	directoryFD, err := openLockedAtomicDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer closeLockedAtomicDirectory(directoryFD)
	if err := validateAtomicPrivateFileName(name); err != nil {
		return err
	}
	if err := reconcileAtomicPrivateFileAt(directoryFD, name); err != nil {
		return err
	}
	temporary, err := createAnonymousPrivateFile(directoryFD, data)
	if err != nil {
		return err
	}
	defer temporary.Close()
	if hook != nil {
		if err := hook(atomicPrivateFileBeforeName); err != nil {
			return err
		}
	}
	stage := atomicPrivateFileStageName(name)
	if err := unix.Linkat(int(temporary.Fd()), "", directoryFD, stage, unix.AT_EMPTY_PATH); err != nil {
		return fmt.Errorf("name complete private atomic stage: %w", err)
	}
	if hook != nil {
		if err := hook(atomicPrivateFileAfterStage); err != nil {
			return err
		}
	}
	if err := unix.Renameat(directoryFD, stage, directoryFD, name); err != nil {
		return fmt.Errorf("publish private atomic replacement: %w", err)
	}
	if hook != nil {
		if err := hook(atomicPrivateFileAfterName); err != nil {
			return err
		}
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return fmt.Errorf("sync private atomic replacement directory: %w", err)
	}
	return verifyAtomicPrivateFileAt(directoryFD, name, data)
}

func readPrivateAtomicFile(path string, limit int64) ([]byte, error) {
	if limit < 0 || limit > atomicPrivateFileMaximumBytes {
		return nil, errors.New("private atomic file read bound is invalid")
	}
	directoryPath, name := filepath.Dir(path), filepath.Base(path)
	directoryFD, err := openLockedAtomicDirectory(directoryPath)
	if err != nil {
		return nil, err
	}
	defer closeLockedAtomicDirectory(directoryFD)
	if err := validateAtomicPrivateFileName(name); err != nil {
		return nil, err
	}
	if err := reconcileAtomicPrivateFileAt(directoryFD, name); err != nil {
		return nil, err
	}
	return readAtomicPrivateFileAt(directoryFD, name, limit)
}

func reconcileAtomicPrivateFile(path string) error {
	directoryPath, name := filepath.Dir(path), filepath.Base(path)
	directoryFD, err := openLockedAtomicDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer closeLockedAtomicDirectory(directoryFD)
	if err := validateAtomicPrivateFileName(name); err != nil {
		return err
	}
	return reconcileAtomicPrivateFileAt(directoryFD, name)
}

func reconcileAtomicPrivateFileAt(directoryFD int, name string) error {
	stage := atomicPrivateFileStageName(name)
	stageFD, err := unix.Openat(directoryFD, stage, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open private atomic stage without following links: %w", err)
	}
	if err := validateAtomicPrivateDescriptor(stageFD, atomicPrivateFileMaximumBytes, 1); err != nil {
		_ = unix.Close(stageFD)
		return fmt.Errorf("private atomic stage is unsafe: %w", err)
	}
	if err := unix.Close(stageFD); err != nil {
		return err
	}
	if err := unix.Renameat(directoryFD, stage, directoryFD, name); err != nil {
		return fmt.Errorf("reconcile private atomic stage: %w", err)
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return fmt.Errorf("sync reconciled private atomic stage: %w", err)
	}
	return nil
}

func createAnonymousPrivateFile(directoryFD int, data []byte) (*os.File, error) {
	if int64(len(data)) > atomicPrivateFileMaximumBytes {
		return nil, errors.New("private atomic file exceeds its maximum size")
	}
	fd, err := unix.Openat(directoryFD, ".", unix.O_RDWR|unix.O_CLOEXEC|unix.O_TMPFILE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create anonymous private atomic file: %w", err)
	}
	fail := func(cause error) (*os.File, error) {
		_ = unix.Close(fd)
		return nil, cause
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return fail(fmt.Errorf("protect anonymous private atomic file: %w", err))
	}
	file := os.NewFile(uintptr(fd), "crewfold-anonymous-private-file")
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write anonymous private atomic file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync anonymous private atomic file: %w", err)
	}
	if err := validateAtomicPrivateDescriptor(int(file.Fd()), int64(len(data)), 0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func openLockedAtomicDirectory(path string) (int, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errors.New("private atomic file directory must be canonical and absolute")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, fmt.Errorf("open private atomic file directory without following links: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		_ = unix.Close(fd)
		if err != nil {
			return -1, err
		}
		return -1, fmt.Errorf("private atomic file directory must be a real owner-controlled directory (mode=%#o uid=%d euid=%d)", stat.Mode&0o777, stat.Uid, os.Geteuid())
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("lock private atomic file directory: %w", err)
	}
	return fd, nil
}

func closeLockedAtomicDirectory(fd int) {
	_ = unix.Flock(fd, unix.LOCK_UN)
	_ = unix.Close(fd)
}

func validateAtomicPrivateFileName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return errors.New("private atomic file name is invalid")
	}
	return nil
}

func atomicPrivateFileStageName(name string) string {
	return ".crewfold-atomic-" + name + ".staged"
}

func validateAtomicPrivateDescriptor(fd int, maximumSize, links int64) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != 0o600 || int64(stat.Nlink) != links || stat.Size < 0 || stat.Size > maximumSize {
		return errors.New("private atomic file must be an exact owner-controlled 0600 regular file with the expected link count and size")
	}
	return nil
}

func readAtomicPrivateFileAt(directoryFD int, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := validateAtomicPrivateDescriptor(fd, limit, 1); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("private atomic file exceeds its read bound")
	}
	return data, nil
}

func verifyAtomicPrivateFileAt(directoryFD int, name string, want []byte) error {
	got, err := readAtomicPrivateFileAt(directoryFD, name, int64(len(want)))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("published private atomic file differs from its complete input")
	}
	return nil
}
