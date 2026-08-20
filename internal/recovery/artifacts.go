package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"crewfold/internal/store"
	"golang.org/x/sys/unix"
)

const maximumArtifactDiagnosticSamples = 20

// VerifyLiveArtifacts verifies the exact typed artifact closure referenced by
// a canonical database report. Unreferenced private files are warnings because
// they are excluded from backup; missing, corrupt, linked, or unsafe referenced
// content is a failure.
func VerifyLiveArtifacts(ctx context.Context, sourceDataDir string, references []store.ImmutableArtifactReference) (ArtifactFilesystemReport, error) {
	report := ArtifactFilesystemReport{
		Status: "failed", Issues: []ArtifactFilesystemIssue{}, Warnings: []ArtifactFilesystemIssue{},
	}
	sourceDataDir, err := exactSelectedRecoveryPath(sourceDataDir)
	if err != nil {
		return report, &Error{Code: CodeBackupSourceUnhealthy, Message: "source data directory path is invalid", Cause: err}
	}
	root, rootStat, err := openAbsoluteDirectoryNoFollow(sourceDataDir)
	if err != nil {
		return report, &Error{Code: CodeBackupSourceUnhealthy, Message: "open source data directory without following links", Cause: err}
	}
	defer root.Close()
	if rootStat.Uid != uint32(os.Geteuid()) || rootStat.Mode&0o777 != bundleDirectoryMode {
		report.UnsafeCount++
		appendArtifactIssue(&report, false, ArtifactFilesystemIssue{
			Code: "unsafe_mode", Path: ".", Detail: "data directory must be exact owner-controlled mode 0700",
		})
	}
	if available, statErr := freeBytes(root); statErr != nil {
		appendArtifactIssue(&report, false, ArtifactFilesystemIssue{Code: "resource_probe_failed", Path: ".", Detail: "free-space probe failed"})
	} else {
		report.FreeBytes = available
	}

	entries := ArtifactEntries(references)
	expected := map[string]ArtifactEntry{}
	for _, entry := range entries {
		if !validArtifactEntry(entry) {
			return report, &Error{Code: CodeBackupSourceUnhealthy, Message: "canonical report contains an invalid artifact reference"}
		}
		if _, exists := expected[entry.Path]; exists {
			return report, &Error{Code: CodeBackupSourceUnhealthy, Message: "canonical report contains a duplicate typed artifact reference"}
		}
		expected[entry.Path] = entry
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.CheckedCount++
		size, digest, hashErr := hashSecureRegular(ctx, root, entry.Path, maximumArtifactSize)
		if hashErr != nil {
			code, detail := "unsafe_artifact", "referenced artifact is unsafe or unreadable"
			if errors.Is(hashErr, os.ErrNotExist) || errors.Is(hashErr, unix.ENOENT) {
				code, detail = "missing_artifact", "referenced artifact is missing"
				report.MissingCount++
			} else {
				report.UnsafeCount++
			}
			appendArtifactIssue(&report, false, ArtifactFilesystemIssue{Code: code, Kind: entry.Kind, Path: entry.Path, Detail: detail})
			continue
		}
		if size != entry.Size || digest != entry.SHA256 {
			report.HashMismatchCount++
			appendArtifactIssue(&report, false, ArtifactFilesystemIssue{
				Code: "artifact_hash_mismatch", Kind: entry.Kind, Path: entry.Path, Detail: "referenced artifact size or SHA-256 differs from the database closure",
			})
		}
	}

	complete := true
	for _, namespace := range []string{"check-artifacts", "run-artifacts", "service-artifacts"} {
		if err := inspectArtifactNamespace(ctx, root, namespace, expected, &report); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return report, ctxErr
			}
			report.UnsafeCount++
			complete = false
			appendArtifactIssue(&report, false, ArtifactFilesystemIssue{
				Code: "unsafe_artifact_tree", Path: namespace, Detail: "artifact namespace could not be completely traversed without following links",
			})
			continue
		}
	}
	report.Complete = complete
	switch {
	case report.IssueCount != 0:
		report.Status = "failed"
	case report.WarningCount != 0:
		report.Status = "warning"
	default:
		report.Status = "ok"
	}
	return report, nil
}

func inspectArtifactNamespace(ctx context.Context, root *secureDirectory, namespace string, expected map[string]ArtifactEntry, report *ArtifactFilesystemReport) error {
	directory, _, err := root.openRelativeDirectory(namespace)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	tree, err := walkSecureTree(ctx, directory)
	if err != nil {
		return err
	}
	expectedDirectories := map[string]bool{".": true}
	for path := range expected {
		if filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))) == "." || !strings.HasPrefix(path, namespace+"/") {
			continue
		}
		relative := path[len(namespace)+1:]
		expectedDirectories[filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))] = true
	}
	paths := make([]string, 0, len(tree.files))
	for path := range tree.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		path := namespace + "/" + relative
		if _, exists := expected[path]; exists {
			continue
		}
		report.ExtraCount++
		appendArtifactIssue(report, true, ArtifactFilesystemIssue{
			Code: "orphan_artifact", Path: path, Detail: "private artifact is not referenced by the captured canonical closure and will be excluded",
		})
	}
	directories := make([]string, 0, len(tree.directories))
	for path := range tree.directories {
		directories = append(directories, path)
	}
	sort.Strings(directories)
	for _, relative := range directories {
		if expectedDirectories[relative] {
			continue
		}
		path := namespace
		if relative != "." {
			path += "/" + relative
		}
		report.ExtraCount++
		appendArtifactIssue(report, true, ArtifactFilesystemIssue{
			Code: "orphan_artifact_directory", Path: path, Detail: "private artifact directory is outside the referenced closure and will be excluded",
		})
	}
	return nil
}

func appendArtifactIssue(report *ArtifactFilesystemReport, warning bool, issue ArtifactFilesystemIssue) {
	if warning {
		report.WarningCount++
		if len(report.Warnings) < maximumArtifactDiagnosticSamples {
			report.Warnings = append(report.Warnings, issue)
		}
		return
	}
	report.IssueCount++
	if len(report.Issues) < maximumArtifactDiagnosticSamples {
		report.Issues = append(report.Issues, issue)
	}
}

func artifactReportFailure(report ArtifactFilesystemReport) error {
	if !report.Complete || report.IssueCount != 0 {
		return &Error{Code: CodeBackupSourceUnhealthy, Message: fmt.Sprintf("source artifact closure failed verification with %d issues", report.IssueCount)}
	}
	return nil
}
