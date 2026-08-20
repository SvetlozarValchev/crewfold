package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	checkArtifactNamespace   = "check-artifacts"
	runArtifactNamespace     = "run-artifacts"
	serviceArtifactNamespace = "service-artifacts"

	mutationAfterImmutableArtifactNamespaceParentSync = "after_immutable_artifact_namespace_parent_sync"
	mutationAfterImmutableArtifactShardParentSync     = "after_immutable_artifact_shard_parent_sync"
	mutationAfterImmutableArtifactContentSync         = "after_immutable_artifact_content_sync"
	mutationAfterImmutableArtifactNamePublish         = "after_immutable_artifact_name_publish"
	mutationAfterImmutableArtifactPublishSync         = "after_immutable_artifact_publish_sync"
)

// publishImmutableArtifact publishes content without following links. The
// caller chooses a typed physical namespace; identical digests used by check
// and run artifacts intentionally produce one file in each namespace.
func (s *Store) publishImmutableArtifact(namespace, _ string, hash string, content []byte, limit int64) error {
	shardFD, err := s.openImmutableArtifactShard(namespace, hash, true)
	if err != nil {
		return err
	}
	defer unix.Close(shardFD)
	if existing, err := readImmutableArtifactAt(shardFD, hash, limit); err == nil {
		sum := sha256.Sum256(existing)
		if len(existing) != len(content) || hex.EncodeToString(sum[:]) != hash {
			return errors.New("existing immutable artifact is corrupt")
		}
		return s.syncImmutableArtifactDirectory(shardFD, mutationAfterImmutableArtifactPublishSync)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporaryFD, err := unix.Openat(shardFD, ".", unix.O_WRONLY|unix.O_TMPFILE|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	temporary := os.NewFile(uintptr(temporaryFD), "anonymous immutable artifact")
	defer temporary.Close()
	written, writeErr := temporary.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	if writeErr != nil {
		return writeErr
	}
	if err := s.runMutationHook(mutationAfterImmutableArtifactContentSync); err != nil {
		return err
	}
	published := true
	if err := unix.Linkat(temporaryFD, "", shardFD, hash, unix.AT_EMPTY_PATH); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		published = false
	}
	if published {
		// O_TMPFILE keeps the fully synced payload outside the namespace until
		// this single no-replace link. A kill therefore leaves either no name or
		// exactly the canonical digest name, never an orphan or second alias.
		if err := s.runMutationHook(mutationAfterImmutableArtifactNamePublish); err != nil {
			return err
		}
	}
	if err := unix.Fsync(shardFD); err != nil {
		return err
	}
	existing, readErr := readImmutableArtifactAt(shardFD, hash, limit)
	if readErr != nil {
		if published {
			return errors.New("published immutable artifact is not an exact single-link bounded regular file")
		}
		return errors.New("raced immutable artifact is not an exact single-link bounded regular file")
	}
	existingDigest := sha256.Sum256(existing)
	if len(existing) != len(content) || hex.EncodeToString(existingDigest[:]) != hash {
		if published {
			return errors.New("published immutable artifact is corrupt")
		}
		return errors.New("raced immutable artifact is corrupt")
	}
	return s.runMutationHook(mutationAfterImmutableArtifactPublishSync)
}

func (s *Store) openImmutableArtifactShard(namespace, hash string, create bool) (int, error) {
	if namespace != checkArtifactNamespace && namespace != runArtifactNamespace && namespace != serviceArtifactNamespace {
		return -1, errors.New("immutable artifact namespace is invalid")
	}
	if !validLowerSHA256(hash) {
		return -1, errors.New("immutable artifact digest is invalid")
	}
	dataDirectory := filepath.Dir(s.path)
	if !filepath.IsAbs(dataDirectory) || filepath.Clean(dataDirectory) != dataDirectory {
		return -1, errors.New("immutable artifact data directory is invalid")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.Trim(dataDirectory, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	for index, component := range []string{namespace, hash[:2]} {
		if create {
			mkdirErr := unix.Mkdirat(fd, component, 0o700)
			if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				_ = unix.Close(fd)
				return -1, mkdirErr
			}
			stage := mutationAfterImmutableArtifactNamespaceParentSync
			if index == 1 {
				stage = mutationAfterImmutableArtifactShardParentSync
			}
			// Sync even after EEXIST: another publisher may have created the
			// directory but not yet made its parent entry durable.
			if syncErr := s.syncImmutableArtifactDirectory(fd, stage); syncErr != nil {
				_ = unix.Close(fd)
				return -1, syncErr
			}
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		var stat unix.Stat_t
		if statErr := unix.Fstat(next, &stat); statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
			_ = unix.Close(next)
			if statErr != nil {
				return -1, statErr
			}
			return -1, errors.New("immutable artifact directory is not private and owner-controlled")
		}
		fd = next
	}
	return fd, nil
}

func (s *Store) syncImmutableArtifactDirectory(fd int, stage string) error {
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	return s.runMutationHook(stage)
}

func readImmutableArtifactAt(directoryFD int, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 || stat.Size < 0 || stat.Size > limit {
		_ = unix.Close(fd)
		return nil, errors.New("immutable artifact must be a bounded private owner-controlled single-link regular file")
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("immutable artifact exceeds its bound")
	}
	return content, nil
}
