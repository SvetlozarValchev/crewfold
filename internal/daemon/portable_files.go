package daemon

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"crewfold/internal/store"

	"golang.org/x/sys/unix"
)

const (
	portableManifestName = "manifest.json"
	portableMarkdownName = "knowledge.md"
	portableFileLimit    = 64 << 20

	codeKnowledgeExportPathExists  = store.CodeKnowledgeExportPathExists
	codeInvalidKnowledgeBundlePath = store.CodeInvalidKnowledgeBundlePath
	codeInvalidKnowledgeBundle     = store.CodeInvalidKnowledgeBundle
)

type portableFileError struct {
	code    string
	message string
	cause   error
}

func (e *portableFileError) Error() string {
	if e.cause == nil {
		return e.message
	}
	return fmt.Sprintf("%s: %v", e.message, e.cause)
}

func (e *portableFileError) Unwrap() error { return e.cause }

func newPortableFileError(code, message string, cause error) error {
	return &portableFileError{code: code, message: message, cause: cause}
}

func portableFileErrorCode(err error) string {
	var pathError *portableFileError
	if errors.As(err, &pathError) {
		return pathError.code
	}
	return store.CodeStorageFailed
}

// validatePortableDirectoryPath deliberately accepts only the exact path sent on
// the wire. Both the CLI and daemon apply this rule so logs, results, and the
// descriptor-relative traversal all name the same directory.
func validatePortableDirectoryPath(path string) error {
	if path == "" || len(path) > 4096 || !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 {
		return newPortableFileError(codeInvalidKnowledgeBundlePath, "knowledge bundle directory must be non-empty UTF-8 without NUL", nil)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return newPortableFileError(codeInvalidKnowledgeBundlePath, "knowledge bundle directory must be an absolute clean non-root path", nil)
	}
	return nil
}

// openPortableParent pins every existing prefix beneath / and refuses symbolic
// links at every step. The returned descriptor is owned by the caller.
func openPortableParent(path string) (int, string, error) {
	return openPortableParentWithPolicy(path, false)
}

// openPortableExportParent additionally rejects ancestors whose directory
// entries can be renamed by an unrelated UID. Sticky shared directories such as
// /tmp remain valid: their owner and sticky bit protect Crewfold's staging entry.
func openPortableExportParent(path string) (int, string, error) {
	return openPortableParentWithPolicy(path, true)
}

func openPortableParentWithPolicy(path string, requireSafePublishAncestors bool) (int, string, error) {
	if err := validatePortableDirectoryPath(path); err != nil {
		return -1, "", err
	}
	parentPath, leaf := filepath.Split(path)
	leaf = strings.TrimSuffix(leaf, string(filepath.Separator))
	if leaf == "" || leaf == "." || leaf == ".." || strings.ContainsRune(leaf, filepath.Separator) {
		return -1, "", newPortableFileError(codeInvalidKnowledgeBundlePath, "knowledge bundle directory has an invalid leaf name", nil)
	}

	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, "", newPortableFileError(codeInvalidKnowledgeBundlePath, "open filesystem root for knowledge bundle", err)
	}
	protectedByPrivateAncestor := false
	if requireSafePublishAncestors {
		protectedByPrivateAncestor, err = validatePortablePublishAncestor(fd, protectedByPrivateAncestor)
		if err != nil {
			_ = unix.Close(fd)
			return -1, "", err
		}
	}
	for _, component := range strings.Split(strings.Trim(parentPath, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, "", newPortableFileError(codeInvalidKnowledgeBundlePath, "open knowledge bundle parent without following links", openErr)
		}
		if requireSafePublishAncestors {
			protectedByPrivateAncestor, err = validatePortablePublishAncestor(next, protectedByPrivateAncestor)
			if err != nil {
				_ = unix.Close(next)
				return -1, "", err
			}
		}
		fd = next
	}
	return fd, leaf, nil
}

func validatePortablePublishAncestor(fd int, protectedByPrivateAncestor bool) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return false, newPortableFileError(codeInvalidKnowledgeBundlePath, "inspect knowledge export path ancestor", err)
	}
	owner := uint32(os.Geteuid())
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 && stat.Uid != owner {
		return false, newPortableFileError(codeInvalidKnowledgeBundlePath, "knowledge export path ancestors must be directories owned by root or the daemon user", nil)
	}
	// Once an owner-controlled ancestor removes search permission for both group
	// and world, unrelated UIDs cannot reach any descendant entry. Below that
	// boundary, a permissive child does not make the staging name reachable.
	protectedByPrivateAncestor = protectedByPrivateAncestor || stat.Mode&0o011 == 0
	if !protectedByPrivateAncestor && stat.Mode&0o022 != 0 && stat.Mode&unix.S_ISVTX == 0 {
		return false, newPortableFileError(codeInvalidKnowledgeBundlePath, "shared writable knowledge export path ancestors must have the sticky bit", nil)
	}
	return protectedByPrivateAncestor, nil
}

func portableLeafExists(parentFD int, leaf string) (bool, error) {
	var existing unix.Stat_t
	err := unix.Fstatat(parentFD, leaf, &existing, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return false, err
}

// ensurePortableExportPathAbsent runs before the database snapshot so a known
// occupied destination fails without paying the cost of snapshot/rendering. The
// publish path repeats the check and still relies on RENAME_NOREPLACE for races.
func ensurePortableExportPathAbsent(directory string) error {
	parentFD, leaf, err := openPortableExportParent(directory)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	exists, err := portableLeafExists(parentFD, leaf)
	if err != nil {
		return newPortableFileError(codeInvalidKnowledgeBundlePath, "inspect knowledge export directory", err)
	}
	if exists {
		return newPortableFileError(codeKnowledgeExportPathExists, "knowledge export directory already exists", nil)
	}
	return nil
}

func createPortableBundle(directory string, manifest, markdown []byte) error {
	if len(manifest) > portableFileLimit || len(markdown) > portableFileLimit {
		return newPortableFileError(codeInvalidKnowledgeBundle, "knowledge export files may not exceed 64 MiB each", nil)
	}
	parentFD, leaf, err := openPortableExportParent(directory)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)

	exists, statErr := portableLeafExists(parentFD, leaf)
	if statErr != nil {
		return newPortableFileError(codeInvalidKnowledgeBundlePath, "inspect knowledge export directory", statErr)
	}
	if exists {
		return newPortableFileError(codeKnowledgeExportPathExists, "knowledge export directory already exists", nil)
	}

	stage, err := portableStageName()
	if err != nil {
		return newPortableFileError(store.CodeStorageFailed, "create random knowledge export staging name", err)
	}
	if err := unix.Mkdirat(parentFD, stage, 0o700); err != nil {
		return newPortableFileError(store.CodeStorageFailed, "create private knowledge export staging directory", err)
	}
	// A restrictive process umask may create the directory with no search bit,
	// so set its exact mode descriptor-relatively before opening it.
	if err := unix.Fchmodat(parentFD, stage, 0o700, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = unix.Unlinkat(parentFD, stage, unix.AT_REMOVEDIR)
		return newPortableFileError(store.CodeStorageFailed, "set private knowledge export staging directory permissions", err)
	}
	stageFD, err := unix.Openat(parentFD, stage, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Unlinkat(parentFD, stage, unix.AT_REMOVEDIR)
		return newPortableFileError(store.CodeStorageFailed, "open private knowledge export staging directory", err)
	}
	if err := unix.Fchmod(stageFD, 0o700); err != nil {
		_ = unix.Close(stageFD)
		_ = unix.Unlinkat(parentFD, stage, unix.AT_REMOVEDIR)
		return newPortableFileError(store.CodeStorageFailed, "set private knowledge export staging directory permissions", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = unix.Unlinkat(stageFD, portableManifestName, 0)
			_ = unix.Unlinkat(stageFD, portableMarkdownName, 0)
			_ = unix.Close(stageFD)
			_ = unix.Unlinkat(parentFD, stage, unix.AT_REMOVEDIR)
			return
		}
		_ = unix.Close(stageFD)
	}()

	if err := writePortableFile(stageFD, portableManifestName, manifest); err != nil {
		return err
	}
	if err := writePortableFile(stageFD, portableMarkdownName, markdown); err != nil {
		return err
	}
	if err := unix.Fsync(stageFD); err != nil {
		return newPortableFileError(store.CodeStorageFailed, "sync knowledge export staging directory", err)
	}
	if err := unix.Renameat2(parentFD, stage, parentFD, leaf, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
			return newPortableFileError(codeKnowledgeExportPathExists, "knowledge export directory already exists", nil)
		}
		return newPortableFileError(store.CodeStorageFailed, "publish knowledge export directory atomically", err)
	}
	cleanup = false
	if err := unix.Fsync(parentFD); err != nil {
		return newPortableFileError(store.CodeStorageFailed, "sync knowledge export parent directory", err)
	}
	return nil
}

func portableStageName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf(".crewfold-export-%x", random[:]), nil
}

func writePortableFile(directoryFD int, name string, data []byte) error {
	fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return newPortableFileError(store.CodeStorageFailed, "create private knowledge export file", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return newPortableFileError(store.CodeStorageFailed, "wrap knowledge export file descriptor", nil)
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		return newPortableFileError(store.CodeStorageFailed, "set private knowledge export file permissions", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return newPortableFileError(store.CodeStorageFailed, "write knowledge export file", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return newPortableFileError(store.CodeStorageFailed, "sync knowledge export file", err)
	}
	if err := file.Close(); err != nil {
		return newPortableFileError(store.CodeStorageFailed, "close knowledge export file", err)
	}
	return nil
}

type portableBundleFiles struct {
	Manifest []byte
	Markdown []byte
}

func readPortableBundle(directory string) (portableBundleFiles, error) {
	return readPortableBundleWithHook(directory, nil)
}

func readPortableBundleWithHook(directory string, afterFileStat func(string)) (portableBundleFiles, error) {
	parentFD, leaf, err := openPortableParent(directory)
	if err != nil {
		return portableBundleFiles{}, err
	}
	defer unix.Close(parentFD)
	directoryFD, err := unix.Openat(parentFD, leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ENOENT) {
			return portableBundleFiles{}, newPortableFileError(codeInvalidKnowledgeBundlePath, "open knowledge import directory without following links", err)
		}
		return portableBundleFiles{}, newPortableFileError(codeInvalidKnowledgeBundle, "open knowledge import directory", err)
	}
	defer unix.Close(directoryFD)

	var directoryBefore unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryBefore); err != nil {
		return portableBundleFiles{}, newPortableFileError(codeInvalidKnowledgeBundle, "inspect knowledge import directory", err)
	}
	if directoryBefore.Mode&unix.S_IFMT != unix.S_IFDIR || directoryBefore.Mode&0o022 != 0 {
		return portableBundleFiles{}, newPortableFileError(codeInvalidKnowledgeBundle, "knowledge bundle directory must not be group or world writable", nil)
	}
	names, err := portableDirectoryNames(directoryFD)
	if err != nil {
		return portableBundleFiles{}, err
	}
	wantNames := []string{portableMarkdownName, portableManifestName}
	if len(names) != len(wantNames) || names[0] != wantNames[0] || names[1] != wantNames[1] {
		return portableBundleFiles{}, newPortableFileError(codeInvalidKnowledgeBundle, "knowledge bundle must contain exactly manifest.json and knowledge.md", nil)
	}
	manifest, err := readStablePortableFile(directoryFD, portableManifestName, afterFileStat)
	if err != nil {
		return portableBundleFiles{}, err
	}
	defer manifest.file.Close()
	markdown, err := readStablePortableFile(directoryFD, portableMarkdownName, afterFileStat)
	if err != nil {
		return portableBundleFiles{}, err
	}
	defer markdown.file.Close()
	afterNames, err := portableDirectoryNames(directoryFD)
	if err != nil {
		return portableBundleFiles{}, err
	}
	var directoryAfter unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryAfter); err != nil {
		return portableBundleFiles{}, newPortableFileError(codeInvalidKnowledgeBundle, "reinspect knowledge import directory", err)
	}
	if !samePortableDirectory(directoryBefore, directoryAfter) || !equalPortableNames(names, afterNames) {
		return portableBundleFiles{}, newPortableFileError(codeInvalidKnowledgeBundle, "knowledge bundle changed while it was being read", nil)
	}
	// Both descriptors remain pinned until both files and the containing
	// directory have been read. Revalidating them together prevents a writer
	// from changing manifest.json while knowledge.md is being consumed (or vice
	// versa) without changing the directory entries themselves.
	if err := manifest.validateUnchanged(); err != nil {
		return portableBundleFiles{}, err
	}
	if err := markdown.validateUnchanged(); err != nil {
		return portableBundleFiles{}, err
	}
	return portableBundleFiles{Manifest: manifest.data, Markdown: markdown.data}, nil
}

func portableDirectoryNames(directoryFD int) ([]string, error) {
	duplicate, err := unix.Dup(directoryFD)
	if err != nil {
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "duplicate knowledge bundle directory descriptor", err)
	}
	file := os.NewFile(uintptr(duplicate), "knowledge-bundle")
	if file == nil {
		_ = unix.Close(duplicate)
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "wrap knowledge bundle directory descriptor", nil)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "rewind knowledge bundle directory descriptor", err)
	}
	// Only two entries are legal. Reading one sentinel entry beyond that bound
	// makes hostile extra-filled directories a constant-memory rejection.
	entries, readErr := file.ReadDir(3)
	closeErr := file.Close()
	if readErr != nil {
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "read knowledge bundle directory", readErr)
	}
	if closeErr != nil {
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "close knowledge bundle directory descriptor", closeErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

type stablePortableFile struct {
	file   *os.File
	before unix.Stat_t
	data   []byte
}

func readStablePortableFile(directoryFD int, name string, afterStat func(string)) (*stablePortableFile, error) {
	// O_NONBLOCK prevents a hostile FIFO from stalling the daemon before fstat
	// proves that the descriptor names a regular file.
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, newPortableFileError(codeInvalidKnowledgeBundlePath, "knowledge bundle file must not be a symbolic link", err)
		}
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "open knowledge bundle file without following links", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "wrap knowledge bundle file descriptor", nil)
	}

	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		_ = file.Close()
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "inspect knowledge bundle file", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Mode&0o022 != 0 || before.Size < 0 || before.Size > portableFileLimit {
		_ = file.Close()
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "knowledge bundle entries must be private regular single-link files no larger than 64 MiB", nil)
	}
	if afterStat != nil {
		afterStat(name)
	}
	data, err := io.ReadAll(io.LimitReader(file, portableFileLimit+1))
	if err != nil {
		_ = file.Close()
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "read knowledge bundle file", err)
	}
	if len(data) > portableFileLimit {
		_ = file.Close()
		return nil, newPortableFileError(codeInvalidKnowledgeBundle, "knowledge bundle file exceeds 64 MiB", nil)
	}
	stable := &stablePortableFile{file: file, before: before, data: data}
	if err := stable.validateUnchanged(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return stable, nil
}

func (file *stablePortableFile) validateUnchanged() error {
	var after unix.Stat_t
	if err := unix.Fstat(int(file.file.Fd()), &after); err != nil {
		return newPortableFileError(codeInvalidKnowledgeBundle, "reinspect knowledge bundle file", err)
	}
	if !samePortableFile(file.before, after) || int64(len(file.data)) != file.before.Size {
		return newPortableFileError(codeInvalidKnowledgeBundle, "knowledge bundle file changed while it was being read", nil)
	}
	return nil
}

func samePortableFile(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Nlink == right.Nlink &&
		left.Uid == right.Uid && left.Gid == right.Gid && left.Rdev == right.Rdev && left.Size == right.Size &&
		left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func samePortableDirectory(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Nlink == right.Nlink &&
		left.Uid == right.Uid && left.Gid == right.Gid && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func equalPortableNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
