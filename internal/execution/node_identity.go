package execution

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	nodeIdentityFileName = "node.id"
	nodeKeyFileName      = "node.key"
)

var nodeIDPattern = regexp.MustCompile("^[0-9a-f]{32}$")

// LoadOrCreateNodeID returns the private local identity used to bind live
// runtime handles. Backup and restore exclude this file; activation creates a
// new identity before runtime drivers or workers are started.
func LoadOrCreateNodeID(dataDirectory string) (string, error) {
	return loadOrCreateNodeID(dataDirectory, false)
}

// CreateNodeID exclusively creates the identity used during restore
// activation. Any preexisting path is rejected rather than reused.
func CreateNodeID(dataDirectory string) (string, error) {
	return loadOrCreateNodeID(dataDirectory, true)
}

func loadOrCreateNodeID(dataDirectory string, exclusive bool) (string, error) {
	if strings.TrimSpace(dataDirectory) == "" {
		return "", errors.New("node identity data directory is invalid")
	}
	dataDirectory, err := filepath.Abs(dataDirectory)
	if err != nil || !filepath.IsAbs(dataDirectory) || filepath.Clean(dataDirectory) != dataDirectory {
		return "", errors.New("node identity data directory is invalid")
	}
	if info, err := os.Lstat(dataDirectory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("node identity data directory must be a real directory")
	}
	path := filepath.Join(dataDirectory, nodeIdentityFileName)
	if !exclusive {
		if identity, err := readNodeID(path); err == nil {
			return identity, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	} else if _, err := os.Lstat(path); err == nil {
		return "", errors.New("node identity path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect node identity path: %w", err)
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate node identity: %w", err)
	}
	identity := hex.EncodeToString(random)
	if err := publishPrivateFileNoReplace(dataDirectory, nodeIdentityFileName, []byte(identity+"\n"), nil); err != nil {
		if errors.Is(err, errAtomicPrivateFileExists) {
			if exclusive {
				return "", errors.New("node identity path already exists")
			}
			return readNodeID(path)
		}
		return "", fmt.Errorf("create node identity: %w", err)
	}
	return identity, nil
}

// LoadOrCreateNodeKey returns the private 32-byte key used to derive scoped
// run capabilities on this node.
func LoadOrCreateNodeKey(dataDirectory string) ([]byte, error) {
	return loadOrCreateNodeKey(dataDirectory, false)
}

// CreateNodeKey exclusively creates a fresh key for restore activation.
func CreateNodeKey(dataDirectory string) ([]byte, error) {
	return loadOrCreateNodeKey(dataDirectory, true)
}

func loadOrCreateNodeKey(dataDirectory string, exclusive bool) ([]byte, error) {
	if strings.TrimSpace(dataDirectory) == "" {
		return nil, errors.New("node key data directory is invalid")
	}
	dataDirectory, err := filepath.Abs(dataDirectory)
	if err != nil || !filepath.IsAbs(dataDirectory) || filepath.Clean(dataDirectory) != dataDirectory {
		return nil, errors.New("node key data directory is invalid")
	}
	if info, err := os.Lstat(dataDirectory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("node key data directory must be a real directory")
	}
	path := filepath.Join(dataDirectory, nodeKeyFileName)
	if !exclusive {
		if key, err := readNodeKey(path); err == nil {
			return key, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if _, err := os.Lstat(path); err == nil {
		return nil, errors.New("node key path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect node key path: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate node key: %w", err)
	}
	if err := publishPrivateFileNoReplace(dataDirectory, nodeKeyFileName, key, nil); err != nil {
		if errors.Is(err, errAtomicPrivateFileExists) {
			if exclusive {
				return nil, errors.New("node key path already exists")
			}
			return readNodeKey(path)
		}
		return nil, fmt.Errorf("create node key: %w", err)
	}
	return key, nil
}

func readNodeID(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() != 33 {
		return "", errors.New("node identity must be a private canonical regular file")
	}
	data, err := readBoundedRegularFile(path, 33)
	if err != nil {
		return "", fmt.Errorf("read node identity: %w", err)
	}
	identity := strings.TrimSuffix(string(data), "\n")
	if len(data) != 33 || string(data) != identity+"\n" || !nodeIDPattern.MatchString(identity) {
		return "", errors.New("node identity must contain one canonical lowercase identifier")
	}
	return identity, nil
}

func readNodeKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() != 32 {
		return nil, errors.New("node key must be a private 32-byte regular file")
	}
	key, err := readBoundedRegularFile(path, 32)
	if err != nil || len(key) != 32 {
		return nil, errors.New("node key must be a private 32-byte regular file")
	}
	return key, nil
}

func validNodeID(identity string) bool {
	return nodeIDPattern.MatchString(identity)
}

// NodeFingerprint binds the public node identifier to the private node key.
// Backup excludes both inputs; restore activation creates fresh values, so a
// copied database can never make an old live runtime binding current.
func NodeFingerprint(nodeID string, nodeKey []byte) (string, error) {
	if !validNodeID(nodeID) || len(nodeKey) != 32 {
		return "", errors.New("node fingerprint requires a canonical node ID and 32-byte node key")
	}
	keyDigest := sha256.Sum256(nodeKey)
	digest := sha256.Sum256([]byte("crewfold.restore.node-fingerprint.v1\n" + nodeID + "\n" + hex.EncodeToString(keyDigest[:]) + "\n"))
	return hex.EncodeToString(digest[:]), nil
}
