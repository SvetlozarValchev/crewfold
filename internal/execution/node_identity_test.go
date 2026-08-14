package execution

import (
	"os"
	"path/filepath"
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
