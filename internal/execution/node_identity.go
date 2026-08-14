package execution

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

const nodeIdentityFileName = "node.id"

var nodeIDPattern = regexp.MustCompile("^[0-9a-f]{32}$")

// LoadOrCreateNodeID returns the private local identity used to bind live
// runtime handles. Backup and restore exclude this file; activation creates a
// new identity before runtime drivers or workers are started.
func LoadOrCreateNodeID(dataDirectory string) (string, error) {
	dataDirectory, err := filepath.Abs(strings.TrimSpace(dataDirectory))
	if err != nil || !filepath.IsAbs(dataDirectory) || filepath.Clean(dataDirectory) != dataDirectory {
		return "", errors.New("node identity data directory is invalid")
	}
	if info, err := os.Lstat(dataDirectory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("node identity data directory must be a real directory")
	}
	path := filepath.Join(dataDirectory, nodeIdentityFileName)
	if identity, err := readNodeID(path); err == nil {
		return identity, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate node identity: %w", err)
	}
	identity := hex.EncodeToString(random)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readNodeID(path)
	}
	if err != nil {
		return "", fmt.Errorf("create node identity: %w", err)
	}
	if _, err := file.WriteString(identity + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write node identity: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync node identity: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close node identity: %w", err)
	}
	directory, err := os.Open(dataDirectory)
	if err != nil {
		return "", fmt.Errorf("open node identity directory: %w", err)
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return "", fmt.Errorf("sync node identity directory: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close node identity directory: %w", closeErr)
	}
	return identity, nil
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

func validNodeID(identity string) bool {
	return nodeIDPattern.MatchString(identity)
}
