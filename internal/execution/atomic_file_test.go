package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAnonymousPrivateFilePublicationHasNoPreNameLeakAndOneReplayableFinalLink(t *testing.T) {
	directory := t.TempDir()
	interrupted := errors.New("injected publication interruption")
	if err := publishPrivateFileNoReplace(directory, "before", []byte("before\n"), func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileBeforeName {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("publish(before-name interruption) error = %v", err)
	}
	if entries := atomicFileTestEntries(t, directory); len(entries) != 0 {
		t.Fatalf("anonymous pre-name interruption leaked entries: %v", entries)
	}

	data := []byte("complete single-link authority\n")
	if err := publishPrivateFileNoReplace(directory, "after", data, func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileAfterName {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("publish(after-name interruption) error = %v", err)
	}
	if got, err := readPrivateAtomicFile(filepath.Join(directory, "after"), int64(len(data))); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read(after-name interruption) = %q, %v", got, err)
	}
	if entries := atomicFileTestEntries(t, directory); !reflect.DeepEqual(entries, []string{"after"}) {
		t.Fatalf("after-name publication entries = %v, want only final", entries)
	}
	var stat unix.Stat_t
	if err := unix.Stat(filepath.Join(directory, "after"), &stat); err != nil || stat.Nlink != 1 || stat.Mode&0o777 != 0o600 {
		t.Fatalf("after-name final metadata = mode %#o nlink %d, %v", stat.Mode&0o777, stat.Nlink, err)
	}
	if err := publishPrivateFileNoReplace(directory, "after", data, nil); !errors.Is(err, errAtomicPrivateFileExists) {
		t.Fatalf("no-replace replay error = %v", err)
	}
}

func TestAtomicReplacementReconcilesOneCompleteDeterministicStageAfterReopen(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := replacePrivateFileAtomic(path, []byte("old"), nil); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("injected replacement interruption")
	if err := replacePrivateFileAtomic(path, []byte("not-published"), func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileBeforeName {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("replace(before-name interruption) error = %v", err)
	}
	if got, err := readPrivateAtomicFile(path, 64); err != nil || string(got) != "old" {
		t.Fatalf("read after pre-name interruption = %q, %v", got, err)
	}
	if entries := atomicFileTestEntries(t, directory); !reflect.DeepEqual(entries, []string{"state.json"}) {
		t.Fatalf("pre-name replacement leaked entries: %v", entries)
	}

	if err := replacePrivateFileAtomic(path, []byte("staged-complete"), func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileAfterStage {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("replace(after-stage interruption) error = %v", err)
	}
	wantBeforeReopen := []string{atomicPrivateFileStageName("state.json"), "state.json"}
	sort.Strings(wantBeforeReopen)
	if entries := atomicFileTestEntries(t, directory); !reflect.DeepEqual(entries, wantBeforeReopen) {
		t.Fatalf("post-stage entries = %v, want %v", entries, wantBeforeReopen)
	}
	// A fresh reader is the restart/reopen boundary. It promotes only the exact
	// complete private deterministic stage and fsyncs the directory.
	if got, err := readPrivateAtomicFile(path, 64); err != nil || string(got) != "staged-complete" {
		t.Fatalf("reopened staged replacement = %q, %v", got, err)
	}
	if entries := atomicFileTestEntries(t, directory); !reflect.DeepEqual(entries, []string{"state.json"}) {
		t.Fatalf("reopened replacement retained a stage: %v", entries)
	}

	if err := replacePrivateFileAtomic(path, []byte("renamed-complete"), func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileAfterName {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("replace(after-name interruption) error = %v", err)
	}
	if got, err := readPrivateAtomicFile(path, 64); err != nil || string(got) != "renamed-complete" {
		t.Fatalf("read after renamed replacement = %q, %v", got, err)
	}
	if entries := atomicFileTestEntries(t, directory); !reflect.DeepEqual(entries, []string{"state.json"}) {
		t.Fatalf("post-name replacement leaked entries: %v", entries)
	}
}

func TestNodeAndDirectRecordsUseAnonymousOrReconciledAtomicPublication(t *testing.T) {
	interrupted := errors.New("injected authority publication interruption")
	nodeDirectory := t.TempDir()
	nodeID := strings.Repeat("a", 32)
	if err := publishPrivateFileNoReplace(nodeDirectory, nodeIdentityFileName, []byte(nodeID+"\n"), func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileAfterName {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("publish interrupted node identity: %v", err)
	}
	if loaded, err := LoadOrCreateNodeID(nodeDirectory); err != nil || loaded != nodeID {
		t.Fatalf("LoadOrCreateNodeID(after named interruption) = %q, %v", loaded, err)
	}
	if entries := atomicFileTestEntries(t, nodeDirectory); !reflect.DeepEqual(entries, []string{nodeIdentityFileName}) {
		t.Fatalf("node authority entries = %v", entries)
	}

	runDirectory := t.TempDir()
	spec := directSupervisorSpec{
		Schema: directSupervisorSpecSchema, NodeID: nodeID, OperationID: "run_atomic_publication",
		Executable: "/bin/true", WorkingDirectory: runDirectory, OutputByteLimit: 1024,
		DefaultGraceMillis: directDefaultGracePeriod.Milliseconds(),
	}
	if err := sealDirectSpec(&spec); err != nil {
		t.Fatal(err)
	}
	specData, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishPrivateFileNoReplace(runDirectory, "launch.json", specData, func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileAfterName {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("publish interrupted direct spec: %v", err)
	}
	if got, err := readDirectSpec(filepath.Join(runDirectory, "launch.json")); err != nil || got.SpecSHA256 != spec.SpecSHA256 {
		t.Fatalf("read interrupted direct spec = %#v, %v", got, err)
	}

	state := directSupervisorState{
		Schema: directSupervisorStateSchema, NodeID: nodeID, OperationID: spec.OperationID,
		Status: RuntimeStateStarting, SpecSHA256: spec.SpecSHA256,
	}
	state.StateSHA256, err = directStateDigest(state)
	if err != nil {
		t.Fatal(err)
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacePrivateFileAtomic(filepath.Join(runDirectory, "state.json"), stateData, func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileAfterStage {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("stage interrupted direct state: %v", err)
	}
	if got, err := readDirectState(runDirectory); err != nil || got.StateSHA256 != state.StateSHA256 {
		t.Fatalf("read reconciled direct state = %#v, %v", got, err)
	}

	stop := directStopRequest{Schema: directStopRequestSchema, GraceMillis: 100}
	stopData, err := json.Marshal(stop)
	if err != nil {
		t.Fatal(err)
	}
	stopPath := filepath.Join(runDirectory, "stop.json")
	if err := replacePrivateFileAtomic(stopPath, stopData, func(point atomicPrivateFilePoint) error {
		if point == atomicPrivateFileAfterStage {
			return interrupted
		}
		return nil
	}); !errors.Is(err, interrupted) {
		t.Fatalf("stage interrupted direct stop: %v", err)
	}
	if got, err := readDirectStopRequest(stopPath); err != nil || got != stop {
		t.Fatalf("read reconciled direct stop = %#v, %v", got, err)
	}
	for _, entry := range atomicFileTestEntries(t, runDirectory) {
		if strings.HasPrefix(entry, ".crewfold-") {
			t.Fatalf("direct record reconciliation leaked temporary entry %q", entry)
		}
	}
}

func atomicFileTestEntries(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	sort.Strings(names)
	return names
}
