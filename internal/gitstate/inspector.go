// Package gitstate performs bounded, read-only observation of local Git checkouts.
package gitstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"crewfold/internal/domain"
)

const maxGitOutput = 1024 * 1024

type Inspector interface {
	Inspect(context.Context, string) (domain.CheckoutObservation, error)
}

type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type GitInspector struct {
	runner Runner
}

func NewInspector() *GitInspector {
	return &GitInspector{runner: ExecRunner{Executable: "git"}}
}

func NewInspectorWithRunner(runner Runner) *GitInspector {
	return &GitInspector{runner: runner}
}

func (i *GitInspector) Inspect(ctx context.Context, requestedPath string) (domain.CheckoutObservation, error) {
	normalized, err := normalizeExistingDirectory(requestedPath)
	if err != nil {
		return domain.CheckoutObservation{}, err
	}

	inside, err := i.gitText(ctx, normalized, "identify Git work tree", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, normalized, "identify Git work tree")
	}
	if inside != "true" {
		return domain.CheckoutObservation{}, &Error{Code: CodeNotGitRepository, Operation: "path is not inside a Git work tree", Path: normalized}
	}

	topLevel, err := i.gitText(ctx, normalized, "resolve checkout root", "rev-parse", "--show-toplevel")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, normalized, "resolve checkout root")
	}
	topLevel, err = normalizeExistingDirectory(topLevel)
	if err != nil {
		return domain.CheckoutObservation{}, &Error{Code: CodeGitOutputInvalid, Operation: "Git returned an invalid checkout root", Path: normalized, Cause: err}
	}

	gitDir, err := i.gitText(ctx, topLevel, "resolve Git directory", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, topLevel, "resolve Git directory")
	}
	commonDir, err := i.gitText(ctx, topLevel, "resolve Git common directory", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, topLevel, "resolve Git common directory")
	}
	gitDir = filepath.Clean(gitDir)
	commonDir = filepath.Clean(commonDir)
	if !filepath.IsAbs(gitDir) || !filepath.IsAbs(commonDir) {
		return domain.CheckoutObservation{}, &Error{Code: CodeGitOutputInvalid, Operation: "Git returned a non-absolute metadata path", Path: topLevel}
	}

	objectFormat, err := i.gitText(ctx, topLevel, "read object format", "rev-parse", "--show-object-format")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, topLevel, "read object format")
	}
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return domain.CheckoutObservation{}, &Error{Code: CodeGitOutputInvalid, Operation: "Git returned unsupported object format " + objectFormat, Path: topLevel}
	}
	oidLength := 40
	if objectFormat == "sha256" {
		oidLength = 64
	}
	oidPattern := regexp.MustCompile(fmt.Sprintf(`^[0-9a-f]{%d}$`, oidLength))

	rootsOutput, err := i.gitText(ctx, topLevel, "read repository roots", "rev-list", "--max-parents=0", "--all")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, topLevel, "read repository roots")
	}
	rootCommits := strings.Fields(rootsOutput)
	if len(rootCommits) == 0 {
		return domain.CheckoutObservation{}, &Error{Code: CodeGitOutputInvalid, Operation: "repository has no reachable root commit", Path: topLevel}
	}
	for _, root := range rootCommits {
		if !oidPattern.MatchString(root) {
			return domain.CheckoutObservation{}, &Error{Code: CodeGitOutputInvalid, Operation: "Git returned a malformed root commit", Path: topLevel}
		}
	}
	sort.Strings(rootCommits)
	rootCommits = compactStrings(rootCommits)

	headCommit, err := i.gitText(ctx, topLevel, "read checkout HEAD", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, topLevel, "read checkout HEAD")
	}
	if !oidPattern.MatchString(headCommit) {
		return domain.CheckoutObservation{}, &Error{Code: CodeGitOutputInvalid, Operation: "Git returned a malformed HEAD commit", Path: topLevel}
	}
	branch, err := i.gitText(ctx, topLevel, "read checkout branch", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, topLevel, "read checkout branch")
	}
	if branch == "HEAD" {
		branch = ""
	}

	status, err := i.gitBytes(ctx, topLevel, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return domain.CheckoutObservation{}, classifyGitFailure(err, topLevel, "read checkout status")
	}
	dirtyPaths, err := parsePorcelainV2Paths(status)
	if err != nil {
		return domain.CheckoutObservation{}, &Error{Code: CodeGitOutputInvalid, Operation: "parse checkout status", Path: topLevel, Cause: err}
	}

	checkoutKind := "standalone"
	if !samePath(gitDir, commonDir) {
		checkoutKind = "linked_worktree"
	}
	return domain.CheckoutObservation{
		Path:         topLevel,
		Availability: "available",
		CheckoutKind: checkoutKind,
		Branch:       branch,
		HeadCommit:   headCommit,
		Dirty:        len(dirtyPaths) != 0,
		DirtyPaths:   dirtyPaths,
		GitDir:       gitDir,
		GitCommonDir: commonDir,
		Repository: domain.RepositoryObservation{
			Fingerprint:  repositoryFingerprint(objectFormat, rootCommits),
			ObjectFormat: objectFormat,
			RootCommits:  append([]string(nil), rootCommits...),
		},
	}, nil
}

func parsePorcelainV2Paths(status []byte) ([]string, error) {
	if len(status) == 0 {
		return []string{}, nil
	}
	records := bytes.Split(status, []byte{0})
	paths := make([]string, 0, len(records))
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) == 0 {
			continue
		}
		var rawPath string
		switch record[0] {
		case '1':
			fields := bytes.SplitN(record, []byte{' '}, 9)
			if len(fields) != 9 {
				return nil, fmt.Errorf("malformed ordinary status record")
			}
			rawPath = string(fields[8])
		case '2':
			fields := bytes.SplitN(record, []byte{' '}, 10)
			if len(fields) != 10 || index+1 >= len(records) || len(records[index+1]) == 0 {
				return nil, fmt.Errorf("malformed rename or copy status record")
			}
			rawPath = string(fields[9])
			index++ // The following NUL record is the original path.
		case 'u':
			fields := bytes.SplitN(record, []byte{' '}, 11)
			if len(fields) != 11 {
				return nil, fmt.Errorf("malformed unmerged status record")
			}
			rawPath = string(fields[10])
		case '?':
			if len(record) < 3 || record[1] != ' ' {
				return nil, fmt.Errorf("malformed untracked status record")
			}
			rawPath = string(record[2:])
		case '!':
			continue
		case '#':
			continue
		default:
			return nil, fmt.Errorf("unknown status record %q", record[0])
		}
		normalized, err := normalizeGitRelativePath(rawPath)
		if err != nil {
			return nil, err
		}
		paths = append(paths, normalized)
	}
	sort.Strings(paths)
	return compactStrings(paths), nil
}

func normalizeGitRelativePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
		return "", fmt.Errorf("Git returned an invalid status path")
	}
	normalized := filepath.ToSlash(filepath.Clean(value))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("Git returned a status path outside the checkout")
	}
	return normalized, nil
}

func (i *GitInspector) gitText(ctx context.Context, path, operation string, arguments ...string) (string, error) {
	data, err := i.gitBytes(ctx, path, arguments...)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", &Error{Code: CodeGitOutputInvalid, Operation: operation + " returned empty output", Path: path}
	}
	if strings.ContainsRune(text, '\x00') {
		return "", &Error{Code: CodeGitOutputInvalid, Operation: operation + " returned NUL data", Path: path}
	}
	return text, nil
}

func (i *GitInspector) gitBytes(ctx context.Context, path string, arguments ...string) ([]byte, error) {
	command := []string{
		"--no-optional-locks",
		"-c", "core.quotepath=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "maintenance.auto=0",
		"-c", "gc.auto=0",
		"-C", path,
	}
	command = append(command, arguments...)
	return i.runner.Run(ctx, command...)
}

type ExecRunner struct {
	Executable string
}

func (r ExecRunner) Run(ctx context.Context, arguments ...string) ([]byte, error) {
	executable := r.Executable
	if executable == "" {
		executable = "git"
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	stdout := &limitedBuffer{limit: maxGitOutput}
	stderr := &limitedBuffer{limit: maxGitOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if errors.Is(stdout.err, errOutputLimit) || errors.Is(stderr.err, errOutputLimit) {
		return nil, &Error{Code: CodeGitOutputInvalid, Operation: "Git output exceeded 1 MiB limit"}
	}
	if err == nil {
		return append([]byte(nil), stdout.Bytes()...), nil
	}
	var executableError *exec.Error
	if errors.As(err, &executableError) || errors.Is(err, os.ErrNotExist) {
		return nil, &Error{Code: CodeGitUnavailable, Operation: "execute Git", Cause: err}
	}
	exitCode := -1
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitCode()
	}
	return nil, &CommandError{ExitCode: exitCode, Stderr: strings.TrimSpace(stderr.String()), Cause: err}
}

func normalizeExistingDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", &Error{Code: CodeCheckoutUnavailable, Operation: "checkout path is empty"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", &Error{Code: CodeCheckoutUnavailable, Operation: "resolve checkout path", Path: path, Cause: err}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", &Error{Code: CodeCheckoutUnavailable, Operation: "resolve checkout path", Path: absolute, Cause: err}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", &Error{Code: CodeCheckoutUnavailable, Operation: "inspect checkout path", Path: resolved, Cause: err}
	}
	if !info.IsDir() {
		return "", &Error{Code: CodeNotGitRepository, Operation: "checkout path is not a directory", Path: resolved}
	}
	return filepath.Clean(resolved), nil
}

func classifyGitFailure(err error, path, operation string) error {
	var gitError *Error
	if errors.As(err, &gitError) {
		if gitError.Path == "" {
			gitError.Path = path
		}
		return gitError
	}
	var commandError *CommandError
	if errors.As(err, &commandError) {
		lower := strings.ToLower(commandError.Stderr)
		if strings.Contains(lower, "not a git repository") {
			return &Error{Code: CodeNotGitRepository, Operation: operation, Path: path, Cause: commandError}
		}
	}
	return &Error{Code: CodeGitCommandFailed, Operation: operation, Path: path, Cause: err}
}

func repositoryFingerprint(objectFormat string, roots []string) string {
	digest := sha256.Sum256([]byte(objectFormat + "\n" + strings.Join(roots, "\n") + "\n"))
	return "git_" + hex.EncodeToString(digest[:])
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func samePath(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

var errOutputLimit = errors.New("output limit exceeded")

type limitedBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	if buffer.err != nil {
		return 0, buffer.err
	}
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 || len(data) > remaining {
		buffer.err = errOutputLimit
		if remaining > 0 {
			_, _ = buffer.Buffer.Write(data[:remaining])
		}
		return len(data), buffer.err
	}
	return buffer.Buffer.Write(data)
}

var _ io.Writer = (*limitedBuffer)(nil)
