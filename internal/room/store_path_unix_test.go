//go:build !windows

package room

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExactDirectoryCanonicalizesSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Fatal(err)
	}

	canonicalReal, err := exactDirectory(realDirectory)
	if err != nil {
		t.Fatal(err)
	}
	canonicalAlias, err := exactDirectory(alias)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalAlias != canonicalReal {
		t.Fatalf("alias = %q, real = %q", canonicalAlias, canonicalReal)
	}
}
