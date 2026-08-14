package execution

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateNodeIDIsPrivateStableAndRecreatedAfterRemoval(t *testing.T) {
	directory := t.TempDir()
	first, err := LoadOrCreateNodeID(directory)
	if err != nil || !validNodeID(first) {
		t.Fatalf("LoadOrCreateNodeID() = %q, %v", first, err)
	}
	second, err := LoadOrCreateNodeID(directory)
	if err != nil || second != first {
		t.Fatalf("LoadOrCreateNodeID(replay) = %q, %v; want %q", second, err, first)
	}
	path := filepath.Join(directory, nodeIdentityFileName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 33 {
		t.Fatalf("node identity info = %#v, %v", info, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove node identity: %v", err)
	}
	recreated, err := LoadOrCreateNodeID(directory)
	if err != nil || !validNodeID(recreated) || recreated == first {
		t.Fatalf("LoadOrCreateNodeID(after removal) = %q, %v; prior %q", recreated, err, first)
	}
}

func TestNodeKeyIsPrivateStableAndExclusiveActivationRejectsExistingPath(t *testing.T) {
	directory := t.TempDir()
	first, err := LoadOrCreateNodeKey(directory)
	if err != nil || len(first) != 32 {
		t.Fatalf("LoadOrCreateNodeKey() = %x, %v", first, err)
	}
	second, err := LoadOrCreateNodeKey(directory)
	if err != nil || !bytes.Equal(second, first) {
		t.Fatalf("LoadOrCreateNodeKey(replay) = %x, %v; want %x", second, err, first)
	}
	info, err := os.Lstat(filepath.Join(directory, nodeKeyFileName))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 32 {
		t.Fatalf("node key info = %#v, %v", info, err)
	}
	if _, err := CreateNodeKey(directory); err == nil {
		t.Fatal("CreateNodeKey(existing path) error = nil")
	}
	if _, err := LoadOrCreateNodeID(directory); err != nil {
		t.Fatalf("LoadOrCreateNodeID() error = %v", err)
	}
	if _, err := CreateNodeID(directory); err == nil {
		t.Fatal("CreateNodeID(existing path) error = nil")
	}
}

func TestExclusiveNodeActivationCreatesFreshIdentityAndKey(t *testing.T) {
	directory := t.TempDir()
	identity, err := CreateNodeID(directory)
	if err != nil || !validNodeID(identity) {
		t.Fatalf("CreateNodeID() = %q, %v", identity, err)
	}
	key, err := CreateNodeKey(directory)
	if err != nil || len(key) != 32 {
		t.Fatalf("CreateNodeKey() = %x, %v", key, err)
	}
}

func TestExclusiveNodeActivationPreservesTrailingSpaceInDataDirectory(t *testing.T) {
	for _, testCase := range []struct {
		name string
		leaf string
	}{
		{name: "ASCII space", leaf: "node-state "},
		{name: "Unicode space", leaf: "node-state\u00a0"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			exact := filepath.Join(root, testCase.leaf)
			adjacent := filepath.Join(root, strings.TrimSpace(testCase.leaf))
			if err := os.Mkdir(exact, 0o700); err != nil {
				t.Fatalf("Mkdir(exact data directory) error = %v", err)
			}
			if err := os.Mkdir(adjacent, 0o700); err != nil {
				t.Fatalf("Mkdir(adjacent data directory) error = %v", err)
			}
			if _, err := CreateNodeID(exact); err != nil {
				t.Fatalf("CreateNodeID(exact path) error = %v", err)
			}
			if _, err := CreateNodeKey(exact); err != nil {
				t.Fatalf("CreateNodeKey(exact path) error = %v", err)
			}
			for _, name := range []string{nodeIdentityFileName, nodeKeyFileName} {
				if info, err := os.Lstat(filepath.Join(exact, name)); err != nil || !info.Mode().IsRegular() {
					t.Fatalf("exact %s info = %#v, %v", name, info, err)
				}
				if _, err := os.Lstat(filepath.Join(adjacent, name)); !os.IsNotExist(err) {
					t.Fatalf("trimmed adjacent path received %s: %v", name, err)
				}
			}
		})
	}
}

func TestLoadOrCreateNodeIDRejectsSymlinkAndNoncanonicalIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		setup func(string) error
	}{
		{name: "symlink", setup: func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, []byte("11111111111111111111111111111111\n"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
		{name: "uppercase", setup: func(path string) error {
			return os.WriteFile(path, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600)
		}},
		{name: "public mode", setup: func(path string) error {
			return os.WriteFile(path, []byte("11111111111111111111111111111111\n"), 0o644)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := testCase.setup(filepath.Join(directory, nodeIdentityFileName)); err != nil {
				t.Fatalf("setup: %v", err)
			}
			if identity, err := LoadOrCreateNodeID(directory); err == nil {
				t.Fatalf("LoadOrCreateNodeID() = %q, nil; want rejection", identity)
			}
		})
	}
}

func TestNodeFingerprintIsStableAndSeparatesIdentityAndKey(t *testing.T) {
	nodeID := "11111111111111111111111111111111"
	key := bytes.Repeat([]byte{0x22}, 32)
	fingerprint, err := NodeFingerprint(nodeID, key)
	if err != nil || len(fingerprint) != 64 {
		t.Fatalf("NodeFingerprint() = %q, %v", fingerprint, err)
	}
	replay, err := NodeFingerprint(nodeID, append([]byte(nil), key...))
	if err != nil || replay != fingerprint {
		t.Fatalf("NodeFingerprint(replay) = %q, %v; want %q", replay, err, fingerprint)
	}

	differentID, err := NodeFingerprint("33333333333333333333333333333333", key)
	if err != nil || differentID == fingerprint {
		t.Fatalf("NodeFingerprint(different ID) = %q, %v; want distinct", differentID, err)
	}
	differentKey, err := NodeFingerprint(nodeID, bytes.Repeat([]byte{0x44}, 32))
	if err != nil || differentKey == fingerprint {
		t.Fatalf("NodeFingerprint(different key) = %q, %v; want distinct", differentKey, err)
	}
}

func TestNodeFingerprintRejectsNoncanonicalInputs(t *testing.T) {
	key := bytes.Repeat([]byte{0x22}, 32)
	for _, nodeID := range []string{
		"",
		" 11111111111111111111111111111111",
		"11111111111111111111111111111111\n",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"1111111111111111111111111111111",
	} {
		if fingerprint, err := NodeFingerprint(nodeID, key); err == nil {
			t.Fatalf("NodeFingerprint(%q) = %q, nil; want rejection", nodeID, fingerprint)
		}
	}
	if fingerprint, err := NodeFingerprint("11111111111111111111111111111111", key[:31]); err == nil {
		t.Fatalf("NodeFingerprint(short key) = %q, nil; want rejection", fingerprint)
	}
}
