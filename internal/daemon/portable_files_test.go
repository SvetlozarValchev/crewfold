package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPortableBundleFilesystemRoundTripIsPrivateAndNoReplace(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "bundle")
	manifest := []byte(`{"schema":"test"}`)
	markdown := []byte("# Knowledge\n")
	if err := createPortableBundle(directory, manifest, markdown); err != nil {
		t.Fatalf("createPortableBundle() error = %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("bundle directory mode=%v error=%v, want 0700", info.Mode(), err)
	}
	for _, name := range []string{portableManifestName, portableMarkdownName} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v error=%v, want regular 0600", name, info.Mode(), err)
		}
	}
	files, err := readPortableBundle(directory)
	if err != nil || string(files.Manifest) != string(manifest) || string(files.Markdown) != string(markdown) {
		t.Fatalf("readPortableBundle()=%#v error=%v", files, err)
	}
	if err := createPortableBundle(directory, []byte("replacement"), markdown); portableFileErrorCode(err) != codeKnowledgeExportPathExists {
		t.Fatalf("second create error=%v code=%q, want %s", err, portableFileErrorCode(err), codeKnowledgeExportPathExists)
	}
	got, err := os.ReadFile(filepath.Join(directory, portableManifestName))
	if err != nil || string(got) != string(manifest) {
		t.Fatalf("manifest after no-replace=%q error=%v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "bundle" {
		t.Fatalf("parent entries=%v error=%v, want published bundle only", entries, err)
	}
}

func TestPortableBundlePermissionsIgnoreRestrictiveUmask(t *testing.T) {
	// Umask is process-global, so this test is intentionally not parallel.
	directory := filepath.Join(t.TempDir(), "bundle")
	previous := unix.Umask(0o777)
	defer unix.Umask(previous)
	if err := createPortableBundle(directory, []byte("{}"), []byte("# k\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%v error=%v", info.Mode(), err)
	}
	for _, name := range []string{portableManifestName, portableMarkdownName} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v error=%v", name, info.Mode(), err)
		}
	}
}

func TestPortableExportRejectsUnsafeWritableAncestorAndAllowsStickySharedParent(t *testing.T) {
	t.Run("non-sticky writable ancestor", func(t *testing.T) {
		shared, err := os.MkdirTemp("", "crewfold-portable-unsafe-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(shared) })
		if err := os.Chmod(shared, 0o777); err != nil {
			t.Fatal(err)
		}
		privateParent := filepath.Join(shared, "private")
		if err := os.Mkdir(privateParent, 0o700); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(privateParent, "bundle")
		if err := ensurePortableExportPathAbsent(directory); portableFileErrorCode(err) != codeInvalidKnowledgeBundlePath {
			t.Fatalf("ensurePortableExportPathAbsent() error=%v code=%q", err, portableFileErrorCode(err))
		}
		if err := createPortableBundle(directory, []byte("{}"), []byte("# k\n")); portableFileErrorCode(err) != codeInvalidKnowledgeBundlePath {
			t.Fatalf("createPortableBundle() error=%v code=%q", err, portableFileErrorCode(err))
		}
	})

	t.Run("sticky shared parent", func(t *testing.T) {
		shared, err := os.MkdirTemp("", "crewfold-portable-sticky-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(shared) })
		if err := os.Chmod(shared, 0o777|os.ModeSticky); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(shared, "bundle")
		if err := createPortableBundle(directory, []byte("{}"), []byte("# k\n")); err != nil {
			t.Fatalf("createPortableBundle() error=%v", err)
		}
	})
}

func TestPortableBundlePathRefusesSymlinkPrefixesAndLeaf(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	if err := createPortableBundle(filepath.Join(linkedParent, "bundle"), []byte("{}"), []byte("# k\n")); portableFileErrorCode(err) != codeInvalidKnowledgeBundlePath {
		t.Fatalf("symlink-prefix create error=%v code=%q", err, portableFileErrorCode(err))
	}
	leafLink := filepath.Join(root, "leaf")
	if err := os.Symlink(realParent, leafLink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPortableBundle(leafLink); portableFileErrorCode(err) != codeInvalidKnowledgeBundlePath {
		t.Fatalf("symlink-leaf read error=%v code=%q", err, portableFileErrorCode(err))
	}
}

func TestPortableExportPreflightNeverTreatsDanglingSymlinkAsAbsent(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "bundle")
	if err := os.Symlink(filepath.Join(root, "missing"), directory); err != nil {
		t.Fatal(err)
	}
	if err := ensurePortableExportPathAbsent(directory); portableFileErrorCode(err) != codeKnowledgeExportPathExists {
		t.Fatalf("ensurePortableExportPathAbsent() error=%v code=%q", err, portableFileErrorCode(err))
	}
}

func TestPortableBundleImportRejectsExtrasUnsafeModesLinksAndSpecialFiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "extra", mutate: func(t *testing.T, directory string) {
			if err := os.WriteFile(filepath.Join(directory, "extra"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "group writable", mutate: func(t *testing.T, directory string) {
			if err := os.Chmod(filepath.Join(directory, portableManifestName), 0o620); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "writable directory", mutate: func(t *testing.T, directory string) {
			if err := os.Chmod(directory, 0o720); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, directory string) {
			if err := os.Remove(filepath.Join(directory, portableManifestName)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(portableMarkdownName, filepath.Join(directory, portableManifestName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, directory string) {
			outside := filepath.Join(filepath.Dir(directory), "outside")
			if err := os.Link(filepath.Join(directory, portableManifestName), outside); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", mutate: func(t *testing.T, directory string) {
			if runtime.GOOS == "windows" {
				t.Skip("FIFO unavailable")
			}
			if err := os.Remove(filepath.Join(directory, portableManifestName)); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(filepath.Join(directory, portableManifestName), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "bundle")
			if err := createPortableBundle(directory, []byte("{}"), []byte("# k\n")); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, directory)
			_, err := readPortableBundle(directory)
			wantCode := codeInvalidKnowledgeBundle
			if test.name == "symlink" {
				wantCode = codeInvalidKnowledgeBundlePath
			}
			var pathError *portableFileError
			if !errors.As(err, &pathError) || pathError.code != wantCode {
				t.Fatalf("readPortableBundle() error=%v code=%q, want %s", err, portableFileErrorCode(err), wantCode)
			}
		})
	}
}

func TestPortableBundleImportRejectsMutationBetweenFileStats(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "bundle")
	if err := createPortableBundle(directory, []byte("{}"), []byte("# k\n")); err != nil {
		t.Fatal(err)
	}
	mutated := false
	_, err := readPortableBundleWithHook(directory, func(name string) {
		if mutated || name != portableManifestName {
			return
		}
		mutated = true
		file, openErr := os.OpenFile(filepath.Join(directory, name), os.O_APPEND|os.O_WRONLY, 0)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.WriteString(" "); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
	if !mutated || portableFileErrorCode(err) != codeInvalidKnowledgeBundle {
		t.Fatalf("mutated=%t error=%v code=%q", mutated, err, portableFileErrorCode(err))
	}
}

func TestPortableBundleImportRejectsEarlierFileMutationDuringLaterFileRead(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "bundle")
	if err := createPortableBundle(directory, []byte("{}"), []byte("# k\n")); err != nil {
		t.Fatal(err)
	}
	mutated := false
	_, err := readPortableBundleWithHook(directory, func(name string) {
		if mutated || name != portableMarkdownName {
			return
		}
		mutated = true
		file, openErr := os.OpenFile(filepath.Join(directory, portableManifestName), os.O_APPEND|os.O_WRONLY, 0)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.WriteString(" "); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
	if !mutated || portableFileErrorCode(err) != codeInvalidKnowledgeBundle {
		t.Fatalf("mutated=%t error=%v code=%q", mutated, err, portableFileErrorCode(err))
	}
}

func TestPortableBundleImportRejectsDirectoryMutationDuringRead(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "bundle")
	if err := createPortableBundle(directory, []byte("{}"), []byte("# k\n")); err != nil {
		t.Fatal(err)
	}
	mutated := false
	_, err := readPortableBundleWithHook(directory, func(string) {
		if mutated {
			return
		}
		mutated = true
		if writeErr := os.WriteFile(filepath.Join(directory, "extra"), nil, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	})
	if !mutated || portableFileErrorCode(err) != codeInvalidKnowledgeBundle {
		t.Fatalf("mutated=%t error=%v code=%q", mutated, err, portableFileErrorCode(err))
	}
}

func TestPortableDirectoryPathRequiresExactAbsoluteCleanPath(t *testing.T) {
	for _, path := range []string{"", ".", "relative", "/", "/tmp/../tmp/bundle", "/tmp/bundle/", string([]byte{'/', 't', 'm', 'p', 0, 'x'})} {
		if err := validatePortableDirectoryPath(path); err == nil {
			t.Errorf("validatePortableDirectoryPath(%q) succeeded", path)
		}
	}
}
