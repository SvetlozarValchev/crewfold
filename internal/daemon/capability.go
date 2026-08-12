package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"crewfold/internal/execution"
)

const capabilityDomain = "crewfold-run-capability-v1\n"

var scopedRunIDPattern = regexp.MustCompile(`^run_[0-9a-f]{32}$`)

type runCapabilityManager struct {
	secret     []byte
	socketPath string
	directory  string
}

func newRunCapabilityManager(dataDir, socketPath string) (*runCapabilityManager, error) {
	directory := filepath.Join(dataDir, "capabilities")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create capability directory: %w", err)
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("capability directory must be a private, non-symlink directory")
	}
	secret, err := loadOrCreateNodeSecret(filepath.Join(dataDir, "node.key"))
	if err != nil {
		return nil, err
	}
	return &runCapabilityManager{secret: secret, socketPath: socketPath, directory: directory}, nil
}

func loadOrCreateNodeSecret(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("node capability key must be a private 32-byte regular file")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) != 32 {
			return nil, errors.New("node capability key must be a private 32-byte regular file")
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read node capability key: %w", err)
	}
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return nil, fmt.Errorf("generate node capability key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create node capability key: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write node capability key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync node capability key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close node capability key: %w", err)
	}
	return data, nil
}

func (manager *runCapabilityManager) PrepareRunCapability(_ context.Context, runID string) (execution.RunCapabilityAccess, error) {
	if !scopedRunIDPattern.MatchString(runID) {
		return execution.RunCapabilityAccess{}, errors.New("run ID is invalid for a scoped capability")
	}
	token := manager.token(runID)
	path := filepath.Join(manager.directory, runID+".token")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return execution.RunCapabilityAccess{}, errors.New("existing run capability file is invalid")
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || strings.TrimSpace(string(existing)) != token {
			return execution.RunCapabilityAccess{}, errors.New("existing run capability file is invalid")
		}
		return execution.RunCapabilityAccess{SocketPath: manager.socketPath, CapabilityFile: path}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return execution.RunCapabilityAccess{}, fmt.Errorf("read run capability file: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return execution.RunCapabilityAccess{}, fmt.Errorf("create run capability file: %w", err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return execution.RunCapabilityAccess{}, fmt.Errorf("write run capability file: %w", err)
	}
	if err := file.Close(); err != nil {
		return execution.RunCapabilityAccess{}, fmt.Errorf("close run capability file: %w", err)
	}
	return execution.RunCapabilityAccess{SocketPath: manager.socketPath, CapabilityFile: path}, nil
}

func (manager *runCapabilityManager) authenticate(token string) (string, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != "cf1" || !scopedRunIDPattern.MatchString(parts[1]) {
		return "", errors.New("run capability is malformed")
	}
	presented, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("run capability is malformed")
	}
	expected := manager.signature(parts[1])
	if !hmac.Equal(presented, expected) {
		return "", errors.New("run capability signature is invalid")
	}
	return parts[1], nil
}

func (manager *runCapabilityManager) token(runID string) string {
	return "cf1." + runID + "." + base64.RawURLEncoding.EncodeToString(manager.signature(runID))
}

func (manager *runCapabilityManager) signature(runID string) []byte {
	digest := hmac.New(sha256.New, manager.secret)
	_, _ = digest.Write([]byte(capabilityDomain + runID))
	return digest.Sum(nil)
}
