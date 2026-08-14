package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const maximumBundleFilesystemNodes = maximumArtifactEntries*2 + 16

const (
	recoveryParentLockName     = ".crewfold-recovery.lock"
	recoveryStagingPrefix      = ".crewfold-recovery-staging-v1-"
	recoveryStageIntentSchema  = "urn:crewfold:schema:recovery-stage-intent:v1"
	maximumRecoveryIntentBytes = 16 << 10
)

type recoveryStageIntent struct {
	Schema      string `json:"schema"`
	Kind        string `json:"kind"`
	TargetPath  string `json:"target_path"`
	StagingName string `json:"staging_name"`
}

type secureDirectory struct {
	path string
	file *os.File
}

type secureFileMetadata struct {
	mode  uint32
	size  int64
	uid   uint32
	nlink uint64
	dev   uint64
	ino   uint64
}

type secureTree struct {
	files       map[string]secureFileMetadata
	directories map[string]secureFileMetadata
}

type secureTarget struct {
	absolute string
	name     string
	parent   *secureDirectory
}

// pathContains reports whether candidate is root itself or is nested below it.
// Both inputs must already be exact absolute paths. filepath.Rel keeps the
// comparison component-aware, so a sibling such as /data/crewfold-backup is not
// confused with /data/crewfold.
func pathContains(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	if relative == "." {
		return true, nil
	}
	return relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// ValidateSelectedPath validates a caller-selected source, bundle, data
// directory, or publication target. Crewfold's own in-progress stage and
// repair-scratch paths are intentionally handled by lower-level internal
// helpers instead. A selected path may not sit at or below a reserved
// component because its owning maintenance operation may reclaim the entire
// tree after process loss.
func ValidateSelectedPath(path string) (string, error) {
	return exactSelectedRecoveryPath(path)
}

func exactSelectedRecoveryPath(path string) (string, error) {
	absolute, err := exactAbsolutePath(path)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimPrefix(absolute, string(filepath.Separator))
	for _, component := range strings.Split(trimmed, string(filepath.Separator)) {
		if validRecoveryStagingName(component) || validRepairScratchRootName(component) || validRepairScratchStageName(component) {
			return "", errors.New("selected recovery path contains a reserved maintenance component")
		}
	}
	return absolute, nil
}

func openExactPrivateDirectory(path string) (*secureDirectory, error) {
	directory, stat, err := openAbsoluteDirectoryNoFollow(path)
	if err != nil {
		return nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != bundleDirectoryMode {
		directory.Close()
		return nil, errors.New("directory must be owner-controlled with exact mode 0700")
	}
	return directory, nil
}

func openOwnedDirectory(path string) (*secureDirectory, error) {
	directory, stat, err := openAbsoluteDirectoryNoFollow(path)
	if err != nil {
		return nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) {
		directory.Close()
		return nil, fmt.Errorf("directory must be owner-controlled (uid=%d mode=%#o)", stat.Uid, stat.Mode&0o777)
	}
	return directory, nil
}

func openAbsoluteDirectoryNoFollow(path string) (*secureDirectory, unix.Stat_t, error) {
	path, err := exactAbsolutePath(path)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, unix.Stat_t{}, openErr
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		if err != nil {
			return nil, unix.Stat_t{}, err
		}
		return nil, unix.Stat_t{}, errors.New("path is not a directory")
	}
	return &secureDirectory{path: path, file: os.NewFile(uintptr(fd), path)}, stat, nil
}

func exactAbsolutePath(path string) (string, error) {
	if path == "" || len(path) > maximumManifestPathBytes || !utf8.ValidString(path) || strings.ContainsRune(path, 0) ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path must be canonical and absolute")
	}
	return path, nil
}

func (directory *secureDirectory) Close() error {
	if directory == nil || directory.file == nil {
		return nil
	}
	return directory.file.Close()
}

func (directory *secureDirectory) Sync() error {
	if directory == nil || directory.file == nil {
		return errors.New("directory is closed")
	}
	return directory.file.Sync()
}

func duplicateDescriptor(fd uintptr) (int, error) {
	return unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
}

func (directory *secureDirectory) openRelativeFile(path string, flags int, requirePrivateParents bool) (*os.File, secureFileMetadata, error) {
	if directory == nil || directory.file == nil || !validRelativePath(path) {
		return nil, secureFileMetadata{}, errors.New("relative file path is invalid")
	}
	components := strings.Split(path, "/")
	current, err := duplicateDescriptor(directory.file.Fd())
	if err != nil {
		return nil, secureFileMetadata{}, err
	}
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return nil, secureFileMetadata{}, openErr
		}
		if requirePrivateParents {
			metadata, statErr := metadataForDescriptor(next)
			if statErr != nil || metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(os.Geteuid()) || metadata.mode&0o777 != bundleDirectoryMode {
				_ = unix.Close(next)
				if statErr != nil {
					return nil, secureFileMetadata{}, statErr
				}
				return nil, secureFileMetadata{}, errors.New("relative parent directory is not exact private storage")
			}
		}
		current = next
	}
	finalFlags := flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Openat(current, components[len(components)-1], finalFlags, 0)
	_ = unix.Close(current)
	if err != nil {
		return nil, secureFileMetadata{}, err
	}
	metadata, err := metadataForDescriptor(fd)
	if err != nil {
		_ = unix.Close(fd)
		return nil, secureFileMetadata{}, err
	}
	return os.NewFile(uintptr(fd), path), metadata, nil
}

func (directory *secureDirectory) openRelativeDirectory(path string) (*secureDirectory, secureFileMetadata, error) {
	file, metadata, err := directory.openRelativeFile(path, unix.O_RDONLY|unix.O_DIRECTORY, true)
	if err != nil {
		return nil, secureFileMetadata{}, err
	}
	if metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(os.Geteuid()) || metadata.mode&0o777 != bundleDirectoryMode {
		_ = file.Close()
		return nil, secureFileMetadata{}, errors.New("directory is not exact owner-controlled mode 0700")
	}
	return &secureDirectory{path: filepath.Join(directory.path, filepath.FromSlash(path)), file: file}, metadata, nil
}

func (directory *secureDirectory) createRelativeFile(path string, mode uint32) (*os.File, error) {
	if directory == nil || directory.file == nil || !validRelativePath(path) {
		return nil, errors.New("relative file path is invalid")
	}
	components := strings.Split(path, "/")
	current, err := duplicateDescriptor(directory.file.Fd())
	if err != nil {
		return nil, err
	}
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		metadata, statErr := metadataForDescriptor(next)
		if statErr != nil || metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(os.Geteuid()) || metadata.mode&0o777 != bundleDirectoryMode {
			_ = unix.Close(next)
			if statErr != nil {
				return nil, statErr
			}
			return nil, errors.New("destination parent is not exact private storage")
		}
		current = next
	}
	fd, err := unix.Openat(current, components[len(components)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, mode)
	_ = unix.Close(current)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (directory *secureDirectory) mkdirAll(path string) error {
	if directory == nil || directory.file == nil || !validRelativePath(path) {
		return errors.New("relative directory path is invalid")
	}
	current, err := duplicateDescriptor(directory.file.Fd())
	if err != nil {
		return err
	}
	for _, component := range strings.Split(path, "/") {
		if err := unix.Mkdirat(current, component, bundleDirectoryMode); err != nil && !errors.Is(err, unix.EEXIST) {
			_ = unix.Close(current)
			return err
		}
		if err := unix.Fsync(current); err != nil {
			_ = unix.Close(current)
			return err
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if openErr != nil {
			_ = unix.Close(current)
			return openErr
		}
		metadata, statErr := metadataForDescriptor(next)
		if statErr != nil || metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(os.Geteuid()) || metadata.mode&0o777 != bundleDirectoryMode {
			_ = unix.Close(next)
			_ = unix.Close(current)
			if statErr != nil {
				return statErr
			}
			return errors.New("created directory is not exact private storage")
		}
		_ = unix.Close(current)
		current = next
	}
	syncErr := unix.Fsync(current)
	closeErr := unix.Close(current)
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (directory *secureDirectory) syncRelativeParent(path string) error {
	if directory == nil || directory.file == nil || !validRelativePath(path) {
		return errors.New("relative file path is invalid")
	}
	current, err := duplicateDescriptor(directory.file.Fd())
	if err != nil {
		return err
	}
	components := strings.Split(path, "/")
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return openErr
		}
		current = next
	}
	syncErr := unix.Fsync(current)
	closeErr := unix.Close(current)
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func metadataForDescriptor(fd int) (secureFileMetadata, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return secureFileMetadata{}, err
	}
	return secureFileMetadata{
		mode: stat.Mode, size: stat.Size, uid: stat.Uid, nlink: stat.Nlink,
		dev: uint64(stat.Dev), ino: stat.Ino,
	}, nil
}

func validatePrivateRegular(metadata secureFileMetadata, maximum int64) error {
	if metadata.mode&unix.S_IFMT != unix.S_IFREG || metadata.mode&0o777 != bundleFileMode ||
		metadata.uid != uint32(os.Geteuid()) || metadata.nlink != 1 || metadata.size < 0 || metadata.size > maximum {
		return errors.New("file is not a bounded exact 0600 owner-controlled unaliased regular file")
	}
	return nil
}

func readSecureRegular(directory *secureDirectory, path string, maximum int64) ([]byte, error) {
	file, metadata, err := directory.openRelativeFile(path, unix.O_RDONLY, true)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := validatePrivateRegular(metadata, maximum); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != metadata.size || int64(len(data)) > maximum {
		return nil, errors.New("file changed or exceeded its bound while being read")
	}
	return data, nil
}

func hashSecureRegular(ctx context.Context, directory *secureDirectory, path string, maximum int64) (int64, string, error) {
	file, metadata, err := directory.openRelativeFile(path, unix.O_RDONLY, true)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	if err := validatePrivateRegular(metadata, maximum); err != nil {
		return 0, "", err
	}
	size, digest, err := hashOpenFile(ctx, file, maximum)
	if err != nil {
		return 0, "", err
	}
	if size != metadata.size {
		return 0, "", errors.New("file size changed while being hashed")
	}
	return size, digest, nil
}

func hashOpenFile(ctx context.Context, file *os.File, maximum int64) (int64, string, error) {
	digest := sha256.New()
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			total += int64(read)
			if total > maximum {
				return 0, "", errors.New("file exceeds its bound")
			}
			_, _ = digest.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			return total, hex.EncodeToString(digest.Sum(nil)), nil
		}
		if readErr != nil {
			return 0, "", readErr
		}
	}
}

func walkSecureTree(ctx context.Context, root *secureDirectory) (secureTree, error) {
	result := secureTree{files: map[string]secureFileMetadata{}, directories: map[string]secureFileMetadata{}}
	rootMetadata, err := metadataForDescriptor(int(root.file.Fd()))
	if err != nil {
		return secureTree{}, err
	}
	result.directories["."] = rootMetadata
	nodes := 1
	var walk func(int, string, int) error
	walk = func(directoryFD int, prefix string, depth int) error {
		if depth > 64 {
			return errors.New("filesystem tree exceeds its depth bound")
		}
		listingFD, err := unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		listing := os.NewFile(uintptr(listingFD), prefix)
		defer listing.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			names, readErr := listing.Readdirnames(256)
			sort.Strings(names)
			for _, name := range names {
				if name == "." || name == ".." || strings.Contains(name, "/") || !validRelativePath(name) {
					return fmt.Errorf("filesystem entry name %q is invalid", name)
				}
				relative := name
				if prefix != "." {
					relative = prefix + "/" + name
				}
				if !validRelativePath(relative) {
					return fmt.Errorf("filesystem path %q is invalid", relative)
				}
				nodes++
				if nodes > maximumBundleFilesystemNodes {
					return errors.New("filesystem tree exceeds its node bound")
				}
				var linkStat unix.Stat_t
				if err := unix.Fstatat(directoryFD, name, &linkStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					return err
				}
				switch linkStat.Mode & unix.S_IFMT {
				case unix.S_IFDIR:
					childFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
					if err != nil {
						return err
					}
					metadata, statErr := metadataForDescriptor(childFD)
					if statErr != nil || metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(os.Geteuid()) || metadata.mode&0o777 != bundleDirectoryMode {
						_ = unix.Close(childFD)
						if statErr != nil {
							return statErr
						}
						return fmt.Errorf("directory %q is not exact owner-controlled mode 0700", relative)
					}
					result.directories[relative] = metadata
					if err := walk(childFD, relative, depth+1); err != nil {
						_ = unix.Close(childFD)
						return err
					}
					_ = unix.Close(childFD)
				case unix.S_IFREG:
					fileFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
					if err != nil {
						return err
					}
					metadata, statErr := metadataForDescriptor(fileFD)
					_ = unix.Close(fileFD)
					if statErr != nil {
						return statErr
					}
					if err := validatePrivateRegular(metadata, maximumBundlePayloadBytes); err != nil {
						return fmt.Errorf("file %q: %w", relative, err)
					}
					result.files[relative] = metadata
				default:
					return fmt.Errorf("filesystem path %q is a link or nonregular entry", relative)
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	if err := walk(int(root.file.Fd()), ".", 0); err != nil {
		return secureTree{}, err
	}
	return result, nil
}

func prepareSecureTarget(path string) (*secureTarget, error) {
	result, err := resolveSecureTarget(path)
	if err != nil {
		return nil, err
	}
	if err := result.requireAbsent(); err != nil {
		result.Close()
		return nil, err
	}
	return result, nil
}

func resolveSecureTarget(path string) (*secureTarget, error) {
	path, err := exactSelectedRecoveryPath(path)
	if err != nil || path == string(filepath.Separator) {
		return nil, errors.New("target path must be a non-root canonical absolute path")
	}
	name := filepath.Base(path)
	if name == recoveryParentLockName || validRecoveryStagingName(name) {
		return nil, errors.New("target basename is reserved for recovery coordination")
	}
	parent, err := openOwnedDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	result := &secureTarget{absolute: path, name: name, parent: parent}
	return result, nil
}

func (target *secureTarget) Close() error {
	if target == nil {
		return nil
	}
	return target.parent.Close()
}

func (target *secureTarget) requireAbsent() error {
	var stat unix.Stat_t
	err := unix.Fstatat(int(target.parent.file.Fd()), target.name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return os.ErrExist
	}
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

// createStaging serializes every recovery publication in one parent. It never
// enumerates or deletes grammar-looking siblings. An abandoned directory is
// removed only when the fsynced parent intent proves Crewfold created that
// deterministic stage. The returned function clears the intent
// after ordinary cleanup/publication (when the stage no longer exists) and then
// releases the flock. A process-loss simulation that releases the flock while
// leaving the stage intact deliberately retains the durable proof for retry.
func (target *secureTarget) createStaging(ctx context.Context, kind string) (string, *secureDirectory, func(), error) {
	if target == nil || target.parent == nil || (kind != "backup" && kind != "restore" && kind != "repair") {
		return "", nil, nil, errors.New("recovery staging request is invalid")
	}
	lock, err := openOrCreatePrivateFile(target.parent, recoveryParentLockName)
	if err != nil {
		return "", nil, nil, err
	}
	unlock := func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}
	if err := flockWithContext(ctx, lock); err != nil {
		unlock()
		return "", nil, nil, err
	}
	name := recoveryStagingName(kind, target.absolute)
	wantedIntent := recoveryStageIntent{
		Schema: recoveryStageIntentSchema, Kind: kind, TargetPath: target.absolute, StagingName: name,
	}

	existingIntent, hasIntent, intentErr := readRecoveryStageIntent(lock)
	if intentErr != nil {
		// The program never creates a stage until a complete intent is fsynced.
		// An invalid interrupted intent is therefore retry-cleanable only when
		// this exact deterministic stage is absent.
		present, inspectErr := target.stagingEntryPresent(name)
		if inspectErr != nil || present {
			unlock()
			return "", nil, nil, errors.Join(errors.New("recovery parent intent is invalid while its stage may exist"), intentErr, inspectErr)
		}
		if err := target.clearRecoveryStageIntent(lock); err != nil {
			unlock()
			return "", nil, nil, err
		}
	} else if hasIntent {
		if filepath.Dir(existingIntent.TargetPath) != target.parent.path {
			unlock()
			return "", nil, nil, errors.New("recovery parent intent names a target in another parent")
		}
		present, inspectErr := target.stagingEntryPresent(existingIntent.StagingName)
		if inspectErr != nil {
			unlock()
			return "", nil, nil, inspectErr
		}
		if present {
			if err := target.cleanupStaging(existingIntent.StagingName); err != nil {
				unlock()
				return "", nil, nil, fmt.Errorf("remove intent-owned abandoned recovery stage: %w", err)
			}
			if err := target.parent.Sync(); err != nil {
				unlock()
				return "", nil, nil, err
			}
		}
		// A missing stage is a stale post-cleanup/post-publish intent. Clearing
		// it is safe even when it described a different prior target.
		if err := target.clearRecoveryStageIntent(lock); err != nil {
			unlock()
			return "", nil, nil, err
		}
	}

	present, err := target.stagingEntryPresent(name)
	if err != nil {
		unlock()
		return "", nil, nil, err
	}
	if present {
		unlock()
		return "", nil, nil, errors.New("deterministic recovery stage collides with an unowned existing entry")
	}
	if err := target.writeRecoveryStageIntent(lock, wantedIntent); err != nil {
		unlock()
		return "", nil, nil, err
	}
	if err := unix.Mkdirat(int(target.parent.file.Fd()), name, bundleDirectoryMode); err != nil {
		_ = target.clearRecoveryStageIntent(lock)
		unlock()
		return "", nil, nil, err
	}
	release := func() {
		if present, inspectErr := target.stagingEntryPresent(name); inspectErr == nil && !present {
			if intent, ok, readErr := readRecoveryStageIntent(lock); readErr == nil && ok && intent == wantedIntent {
				_ = target.clearRecoveryStageIntent(lock)
			}
		}
		unlock()
	}
	if err := target.parent.Sync(); err != nil {
		_ = target.cleanupStaging(name)
		release()
		return "", nil, nil, err
	}
	fd, err := unix.Openat(int(target.parent.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		_ = target.cleanupStaging(name)
		release()
		return "", nil, nil, err
	}
	metadata, err := metadataForDescriptor(fd)
	if err != nil || metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(os.Geteuid()) || metadata.mode&0o777 != bundleDirectoryMode {
		_ = unix.Close(fd)
		_ = target.cleanupStaging(name)
		release()
		if err != nil {
			return "", nil, nil, err
		}
		return "", nil, nil, errors.New("recovery staging directory is not exact private storage")
	}
	return name, &secureDirectory{path: filepath.Join(target.parent.path, name), file: os.NewFile(uintptr(fd), name)}, release, nil
}

func recoveryStagingName(kind, targetPath string) string {
	digest := sha256.Sum256([]byte("crewfold.recovery.staging.v1\n" + kind + "\n" + targetPath))
	return recoveryStagingPrefix + hex.EncodeToString(digest[:])
}

func recoveryStagingPath(kind, targetPath string) string {
	return filepath.Join(filepath.Dir(targetPath), recoveryStagingName(kind, targetPath))
}

func readRecoveryStageIntent(lock *os.File) (recoveryStageIntent, bool, error) {
	if lock == nil {
		return recoveryStageIntent{}, false, errors.New("recovery parent lock is missing")
	}
	info, err := lock.Stat()
	if err != nil {
		return recoveryStageIntent{}, false, err
	}
	if info.Size() == 0 {
		return recoveryStageIntent{}, false, nil
	}
	if info.Size() < 0 || info.Size() > maximumRecoveryIntentBytes {
		return recoveryStageIntent{}, false, errors.New("recovery parent intent exceeds its safety bound")
	}
	data := make([]byte, info.Size())
	if _, err := lock.ReadAt(data, 0); err != nil {
		return recoveryStageIntent{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var intent recoveryStageIntent
	if err := decoder.Decode(&intent); err != nil {
		return recoveryStageIntent{}, false, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return recoveryStageIntent{}, false, errors.New("recovery parent intent has trailing JSON")
		}
		return recoveryStageIntent{}, false, err
	}
	canonical, err := marshalRecoveryStageIntent(intent)
	if err != nil || !bytes.Equal(canonical, data) || !validRecoveryStageIntent(intent) {
		return recoveryStageIntent{}, false, errors.New("recovery parent intent is not exact canonical current state")
	}
	return intent, true, nil
}

func marshalRecoveryStageIntent(intent recoveryStageIntent) ([]byte, error) {
	data, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func validRecoveryStageIntent(intent recoveryStageIntent) bool {
	if intent.Schema != recoveryStageIntentSchema ||
		(intent.Kind != "backup" && intent.Kind != "restore" && intent.Kind != "repair") {
		return false
	}
	absolute, err := exactAbsolutePath(intent.TargetPath)
	return err == nil && absolute == intent.TargetPath && absolute != string(filepath.Separator) &&
		intent.StagingName == recoveryStagingName(intent.Kind, intent.TargetPath)
}

func (target *secureTarget) writeRecoveryStageIntent(lock *os.File, intent recoveryStageIntent) error {
	if !validRecoveryStageIntent(intent) {
		return errors.New("recovery stage intent is invalid")
	}
	data, err := marshalRecoveryStageIntent(intent)
	if err != nil {
		return err
	}
	if err := lock.Truncate(0); err != nil {
		return err
	}
	written, err := lock.WriteAt(data, 0)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = lock.Sync()
	}
	if err == nil {
		err = target.parent.Sync()
	}
	return err
}

func (target *secureTarget) clearRecoveryStageIntent(lock *os.File) error {
	if err := lock.Truncate(0); err != nil {
		return err
	}
	if err := lock.Sync(); err != nil {
		return err
	}
	return target.parent.Sync()
}

func (target *secureTarget) stagingEntryPresent(name string) (bool, error) {
	if !validRecoveryStagingName(name) {
		return false, errors.New("recovery staging name is invalid")
	}
	var stat unix.Stat_t
	err := unix.Fstatat(int(target.parent.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return false, err
}

func validRecoveryStagingName(name string) bool {
	digest := strings.TrimPrefix(name, recoveryStagingPrefix)
	if digest == name || len(digest) != sha256.Size*2 {
		return false
	}
	for _, character := range digest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (target *secureTarget) publish(stagingName string) error {
	if err := unix.Renameat2(int(target.parent.file.Fd()), stagingName, int(target.parent.file.Fd()), target.name, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	return target.parent.Sync()
}

func (target *secureTarget) cleanupStaging(name string) error {
	return removeTreeAt(int(target.parent.file.Fd()), name)
}

func removeTreeAt(parentFD int, name string) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), name)
	for {
		names, readErr := directory.Readdirnames(256)
		for _, child := range names {
			var stat unix.Stat_t
			if err := unix.Fstatat(fd, child, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				directory.Close()
				return err
			}
			if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
				if err := removeTreeAt(fd, child); err != nil {
					directory.Close()
					return err
				}
			} else if err := unix.Unlinkat(fd, child, 0); err != nil {
				directory.Close()
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			directory.Close()
			return readErr
		}
	}
	if err := directory.Close(); err != nil {
		return err
	}
	return unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
}

func writeSecureExclusive(directory *secureDirectory, path string, data []byte) error {
	return writeSecureExclusiveWithHooks(directory, path, data, nil, nil)
}

func writeSecureExclusiveWithHooks(directory *secureDirectory, path string, data []byte, afterContentSync, afterNamePublish func() error) error {
	parentFD, name, err := openPrivateRelativeParent(directory, path)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, ".", unix.O_WRONLY|unix.O_TMPFILE|unix.O_CLOEXEC, bundleFileMode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "anonymous recovery file")
	defer file.Close()
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr == nil && afterContentSync != nil {
		writeErr = afterContentSync()
	}
	if writeErr != nil {
		return writeErr
	}
	if err := unix.Linkat(fd, "", parentFD, name, unix.AT_EMPTY_PATH); err != nil {
		return err
	}
	if afterNamePublish != nil {
		if err := afterNamePublish(); err != nil {
			return err
		}
	}
	return unix.Fsync(parentFD)
}

func openPrivateRelativeParent(directory *secureDirectory, path string) (int, string, error) {
	if directory == nil || directory.file == nil || !validRelativePath(path) {
		return -1, "", errors.New("relative file path is invalid")
	}
	components := strings.Split(path, "/")
	current, err := duplicateDescriptor(directory.file.Fd())
	if err != nil {
		return -1, "", err
	}
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, "", openErr
		}
		metadata, statErr := metadataForDescriptor(next)
		if statErr != nil || metadata.mode&unix.S_IFMT != unix.S_IFDIR || metadata.uid != uint32(os.Geteuid()) || metadata.mode&0o777 != bundleDirectoryMode {
			_ = unix.Close(next)
			if statErr != nil {
				return -1, "", statErr
			}
			return -1, "", errors.New("destination parent is not exact private storage")
		}
		current = next
	}
	return current, components[len(components)-1], nil
}

func openOrCreatePrivateFile(directory *secureDirectory, path string) (*os.File, error) {
	if directory == nil || directory.file == nil || !validRelativePath(path) {
		return nil, errors.New("relative file path is invalid")
	}
	components := strings.Split(path, "/")
	current, err := duplicateDescriptor(directory.file.Fd())
	if err != nil {
		return nil, err
	}
	for _, component := range components[:len(components)-1] {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	fd, err := unix.Openat(current, components[len(components)-1], unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, bundleFileMode)
	_ = unix.Close(current)
	if err != nil {
		return nil, err
	}
	metadata, err := metadataForDescriptor(fd)
	if err != nil || validatePrivateRegular(metadata, 1<<20) != nil {
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("maintenance file is not exact private storage")
	}
	if err := directory.syncRelativeParent(path); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func replaceSecureFileAtomic(directory *secureDirectory, name string, data []byte) error {
	if strings.Contains(name, "/") || !validRelativePath(name) {
		return errors.New("atomic private filename is invalid")
	}
	temporary := "." + name + ".replacement"
	if err := removePrivateReplacementIfPresent(directory, temporary); err != nil {
		return err
	}
	if err := writeSecureExclusive(directory, temporary, data); err != nil {
		return err
	}
	defer removePrivateReplacementIfPresent(directory, temporary)
	if err := unix.Renameat(int(directory.file.Fd()), temporary, int(directory.file.Fd()), name); err != nil {
		return err
	}
	return directory.Sync()
}

func removePrivateReplacementIfPresent(directory *secureDirectory, name string) error {
	if directory == nil || directory.file == nil || strings.Contains(name, "/") || !validRelativePath(name) {
		return errors.New("replacement filename is invalid")
	}
	fd, err := unix.Openat(int(directory.file.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	metadata, metadataErr := metadataForDescriptor(fd)
	closeErr := unix.Close(fd)
	if metadataErr != nil {
		return metadataErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := validatePrivateRegular(metadata, maximumManifestSize); err != nil {
		return errors.New("interrupted replacement is not exact private storage")
	}
	if err := unix.Unlinkat(int(directory.file.Fd()), name, 0); err != nil {
		return err
	}
	return directory.Sync()
}

func copySecurePayload(ctx context.Context, source *secureDirectory, sourcePath string, destination *secureDirectory, destinationPath string, expectedSize int64, expectedSHA256 string) error {
	sourceFile, metadata, err := source.openRelativeFile(sourcePath, unix.O_RDONLY, true)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	if err := validatePrivateRegular(metadata, expectedSize); err != nil || metadata.size != expectedSize {
		if err != nil {
			return err
		}
		return errors.New("source payload size differs from its manifest")
	}
	destinationFile, err := destination.createRelativeFile(destinationPath, bundleFileMode)
	if err != nil {
		return err
	}
	digest := sha256.New()
	written, copyErr := copyWithContext(ctx, io.MultiWriter(destinationFile, digest), io.LimitReader(sourceFile, expectedSize+1))
	if copyErr == nil && written != expectedSize {
		copyErr = errors.New("copied payload size differs from its manifest")
	}
	if copyErr == nil && hex.EncodeToString(digest.Sum(nil)) != expectedSHA256 {
		copyErr = errors.New("copied payload SHA-256 differs from its manifest")
	}
	if copyErr == nil {
		copyErr = destinationFile.Sync()
	}
	closeErr := destinationFile.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = destination.syncRelativeParent(destinationPath)
	}
	return copyErr
}

func freeBytes(directory *secureDirectory) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(directory.file.Fd()), &stat); err != nil {
		return 0, err
	}
	if stat.Bavail > ^uint64(0)/uint64(stat.Bsize) {
		return ^uint64(0), nil
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
