//go:build linux

// Package servicehost owns Crewfold's bounded, non-interactive local service
// processes. Canonical lifecycle stays in Store; this package holds only live
// OS authority and validates every PID with its Linux process start tick.
package servicehost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"crewfold/internal/domain"

	"golang.org/x/sys/unix"
)

type Binding struct {
	PID               int
	ProcessGroupID    int
	ProcessStartTicks uint64
	StdoutPath        string
	StderrPath        string
}

type Snapshot struct {
	Running    bool
	Tracked    bool
	ExitKnown  bool
	ExitCode   int
	Diagnostic string
	Health     string
}

type process struct {
	command *exec.Cmd
	done    chan struct{}
	exit    int
	err     error
	stdout  *boundedWriter
	stderr  *boundedWriter
}

type Host struct {
	dataDir string
	mu      sync.Mutex
	active  map[string]*process
}

func New(dataDir string) *Host {
	return &Host{dataDir: dataDir, active: map[string]*process{}}
}

func (h *Host) Start(_ context.Context, instanceID string, generation int, checkoutPath string, definition domain.ManagedServiceDefinition) (Binding, error) {
	workdir, err := openContainedWorkingDirectory(checkoutPath, definition.WorkingDirectory)
	if err != nil {
		return Binding{}, err
	}
	defer workdir.Close()
	executable, err := resolveExecutable(definition.Executable)
	if err != nil {
		return Binding{}, err
	}
	generationFD, err := openServiceGeneration(h.dataDir, instanceID, generation, true)
	if err != nil {
		return Binding{}, err
	}
	defer unix.Close(generationFD)
	stdoutRelative := filepath.ToSlash(filepath.Join("service-runtime", instanceID, strconv.Itoa(generation), "stdout.log"))
	stderrRelative := filepath.ToSlash(filepath.Join("service-runtime", instanceID, strconv.Itoa(generation), "stderr.log"))
	stdout, err := createLogAt(generationFD, "stdout.log")
	if err != nil {
		return Binding{}, err
	}
	stderr, err := createLogAt(generationFD, "stderr.log")
	if err != nil {
		stdout.Close()
		return Binding{}, err
	}
	stdoutWriter := &boundedWriter{file: stdout, remaining: definition.OutputByteLimit}
	stderrWriter := &boundedWriter{file: stderr, remaining: definition.OutputByteLimit}
	command := exec.Command(executable, definition.Arguments...)
	command.Dir = fmt.Sprintf("/proc/self/fd/%d", workdir.Fd())
	command.Env = serviceEnvironment(definition.Environment)
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	command.Stdin = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := command.Start(); err != nil {
		stdout.Close()
		stderr.Close()
		return Binding{}, fmt.Errorf("start managed service: %w", err)
	}
	if err := unix.Fsync(generationFD); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		stdout.Close()
		stderr.Close()
		return Binding{}, fmt.Errorf("sync managed service log directory: %w", err)
	}
	pid := command.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		stdout.Close()
		stderr.Close()
		return Binding{}, fmt.Errorf("read managed service process group: %w", err)
	}
	ticks, err := processStartTicks(pid)
	if err != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = command.Wait()
		stdout.Close()
		stderr.Close()
		return Binding{}, err
	}
	tracked := &process{command: command, done: make(chan struct{}), exit: -1, stdout: stdoutWriter, stderr: stderrWriter}
	h.mu.Lock()
	if _, exists := h.active[instanceID]; exists {
		h.mu.Unlock()
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = command.Wait()
		stdout.Close()
		stderr.Close()
		return Binding{}, errors.New("managed service operation is already active")
	}
	h.active[instanceID] = tracked
	h.mu.Unlock()
	go func() {
		err := command.Wait()
		tracked.err = err
		if command.ProcessState != nil {
			tracked.exit = command.ProcessState.ExitCode()
		}
		_ = stdout.Sync()
		_ = stderr.Sync()
		_ = stdout.Close()
		_ = stderr.Close()
		close(tracked.done)
	}()
	return Binding{PID: pid, ProcessGroupID: pgid, ProcessStartTicks: ticks, StdoutPath: stdoutRelative, StderrPath: stderrRelative}, nil
}

func (h *Host) Inspect(ctx context.Context, instanceID string, binding Binding, health domain.ManagedServiceHealthCheck) Snapshot {
	h.mu.Lock()
	tracked := h.active[instanceID]
	h.mu.Unlock()
	if tracked != nil {
		select {
		case <-tracked.done:
			return Snapshot{Tracked: true, ExitKnown: true, ExitCode: tracked.exit, Diagnostic: exitDiagnostic(tracked.err), Health: domain.ManagedServiceHealthUnhealthy}
		default:
		}
	}
	if err := validateProcess(binding); err != nil {
		// A very short-lived child can disappear from /proc just before the
		// command waiter publishes ProcessState and closes done. Give only that
		// already-tracked waiter a bounded chance to publish the exact exit;
		// untracked or still-ambiguous bindings continue to fail closed.
		if tracked != nil {
			timer := time.NewTimer(50 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-tracked.done:
				return Snapshot{Tracked: true, ExitKnown: true, ExitCode: tracked.exit, Diagnostic: exitDiagnostic(tracked.err), Health: domain.ManagedServiceHealthUnhealthy}
			case <-ctx.Done():
				// The waiter and probe deadline can become ready together for a
				// short-lived child. Prefer the exact ProcessState if it was
				// published at that boundary; otherwise the outcome remains
				// deliberately unknown.
				select {
				case <-tracked.done:
					return Snapshot{Tracked: true, ExitKnown: true, ExitCode: tracked.exit, Diagnostic: exitDiagnostic(tracked.err), Health: domain.ManagedServiceHealthUnhealthy}
				default:
				}
				return Snapshot{Tracked: true, Diagnostic: ctx.Err().Error(), Health: domain.ManagedServiceHealthUnknown}
			case <-timer.C:
				select {
				case <-tracked.done:
					return Snapshot{Tracked: true, ExitKnown: true, ExitCode: tracked.exit, Diagnostic: exitDiagnostic(tracked.err), Health: domain.ManagedServiceHealthUnhealthy}
				default:
				}
			}
		}
		return Snapshot{Diagnostic: err.Error(), Health: domain.ManagedServiceHealthUnknown}
	}
	result := Snapshot{Running: true, Tracked: tracked != nil, Health: domain.ManagedServiceHealthHealthy}
	if err := probe(ctx, health); err != nil {
		result.Health = domain.ManagedServiceHealthUnhealthy
		result.Diagnostic = err.Error()
	}
	return result
}

func (h *Host) Stop(ctx context.Context, instanceID string, binding Binding, grace time.Duration) (bool, error) {
	if err := validateProcess(binding); err != nil {
		return false, err
	}
	if err := syscall.Kill(-binding.ProcessGroupID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return false, fmt.Errorf("signal managed service group: %w", err)
	}
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			if err := syscall.Kill(-binding.ProcessGroupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return true, fmt.Errorf("kill managed service group: %w", err)
			}
			return true, nil
		case <-ticker.C:
			if err := validateProcess(binding); err != nil {
				return false, nil
			}
		}
	}
}

func (h *Host) ReadLogs(instanceID string, binding Binding, limit int64) (stdout, stderr []byte, stdoutOmitted, stderrOmitted int64, err error) {
	boundInstanceID, generation, err := parseServiceLogBinding(binding)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	if boundInstanceID != instanceID {
		return nil, nil, 0, 0, errors.New("managed service log binding belongs to a different instance")
	}
	generationFD, err := openServiceGeneration(h.dataDir, boundInstanceID, generation, false)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	defer unix.Close(generationFD)
	stdout, stdoutOmitted, err = readBoundedAt(generationFD, "stdout.log", limit)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	stderr, stderrOmitted, err = readBoundedAt(generationFD, "stderr.log", limit)
	h.mu.Lock()
	tracked := h.active[instanceID]
	h.mu.Unlock()
	if tracked != nil {
		stdoutOmitted += tracked.stdout.omittedBytes()
		stderrOmitted += tracked.stderr.omittedBytes()
	}
	return
}

func (h *Host) forget(instanceID string) {
	h.mu.Lock()
	delete(h.active, instanceID)
	h.mu.Unlock()
}

// Forget releases this process generation only after the daemon has captured
// its bounded omission counters and terminal log bytes.
func (h *Host) Forget(instanceID string) {
	h.forget(instanceID)
}

// RemoveRuntime removes only the exact fixed-shape node-local runtime tree for
// one terminal instance. It refuses unexpected entries rather than recursively
// deleting an attacker-controlled or future-format tree.
func (h *Host) RemoveRuntime(instanceID string) error {
	if !strings.HasPrefix(instanceID, "svcinst_") || strings.Contains(instanceID, "/") {
		return errors.New("managed service runtime identity is invalid")
	}
	dataFD, err := openDataDirectory(h.dataDir)
	if err != nil {
		return err
	}
	defer unix.Close(dataFD)
	runtimeFD, err := openPrivateDirectoryAt(dataFD, "service-runtime", false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(runtimeFD)
	instanceFD, err := openPrivateDirectoryAt(runtimeFD, instanceID, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	for generation := 1; generation <= 21; generation++ {
		name := strconv.Itoa(generation)
		generationFD, openErr := openPrivateDirectoryAt(instanceFD, name, false)
		if errors.Is(openErr, unix.ENOENT) {
			continue
		}
		if openErr != nil {
			unix.Close(instanceFD)
			return openErr
		}
		for _, logName := range []string{"stdout.log", "stderr.log"} {
			if unlinkErr := unix.Unlinkat(generationFD, logName, 0); unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
				unix.Close(generationFD)
				unix.Close(instanceFD)
				return unlinkErr
			}
		}
		if syncErr := unix.Fsync(generationFD); syncErr != nil {
			unix.Close(generationFD)
			unix.Close(instanceFD)
			return syncErr
		}
		if closeErr := unix.Close(generationFD); closeErr != nil {
			unix.Close(instanceFD)
			return closeErr
		}
		if unlinkErr := unix.Unlinkat(instanceFD, name, unix.AT_REMOVEDIR); unlinkErr != nil {
			unix.Close(instanceFD)
			return unlinkErr
		}
		if syncErr := unix.Fsync(instanceFD); syncErr != nil {
			unix.Close(instanceFD)
			return syncErr
		}
	}
	if err := unix.Close(instanceFD); err != nil {
		return err
	}
	if err := unix.Unlinkat(runtimeFD, instanceID, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove managed service runtime directory: %w", err)
	}
	return unix.Fsync(runtimeFD)
}

func openContainedWorkingDirectory(checkoutPath, relative string) (*os.File, error) {
	if !filepath.IsAbs(checkoutPath) || filepath.Clean(checkoutPath) != checkoutPath {
		return nil, errors.New("managed service checkout must be an absolute clean path")
	}
	relative = filepath.Clean(filepath.FromSlash(relative))
	if relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("managed service working directory escapes its checkout")
	}
	target := filepath.Join(checkoutPath, relative)
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open managed service filesystem root: %w", err)
	}
	for _, component := range strings.Split(strings.Trim(target, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("open managed service working directory without following symbolic links: %w", openErr)
		}
		fd = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Nlink < 1 || stat.Uid != uint32(os.Geteuid()) {
		_ = unix.Close(fd)
		return nil, errors.New("managed service working directory is not an owner-controlled directory")
	}
	file := os.NewFile(uintptr(fd), target)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("adopt managed service working directory descriptor")
	}
	return file, nil
}

func resolveExecutable(value string) (string, error) {
	if filepath.IsAbs(value) {
		return value, nil
	}
	path, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("resolve managed service executable %q: %w", value, err)
	}
	return path, nil
}

func serviceEnvironment(overrides []domain.ManagedServiceEnvironmentVariable) []string {
	keys := []string{"HOME", "LANG", "LC_ALL", "PATH", "TMPDIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"}
	values := map[string]string{}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			values[key] = value
		}
	}
	for _, item := range overrides {
		values[item.Name] = item.Value
	}
	result := make([]string, 0, len(values))
	for _, key := range keys {
		if value, ok := values[key]; ok {
			result = append(result, key+"="+value)
			delete(values, key)
		}
	}
	for _, item := range overrides {
		if value, ok := values[item.Name]; ok {
			result = append(result, item.Name+"="+value)
			delete(values, item.Name)
		}
	}
	return result
}

func createLogAt(directoryFD int, name string) (*os.File, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create managed service log: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("managed service log is not a private owner-controlled file")
	}
	return os.NewFile(uintptr(fd), name), nil
}

type boundedWriter struct {
	mu        sync.Mutex
	file      *os.File
	remaining int64
	omitted   int64
}

func (w *boundedWriter) Write(content []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	requested := len(content)
	if w.remaining <= 0 {
		w.omitted += int64(requested)
		return requested, nil
	}
	write := int64(len(content))
	if write > w.remaining {
		write = w.remaining
	}
	if _, err := w.file.Write(content[:write]); err != nil {
		return 0, err
	}
	w.remaining -= write
	w.omitted += int64(requested) - write
	return requested, nil
}

func (w *boundedWriter) omittedBytes() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.omitted
}

func processStartTicks(pid int) (uint64, error) {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("read managed service process identity: %w", err)
	}
	end := strings.LastIndexByte(string(content), ')')
	if end < 0 {
		return 0, errors.New("managed service process identity is malformed")
	}
	fields := strings.Fields(string(content[end+1:]))
	if len(fields) <= 19 {
		return 0, errors.New("managed service process identity is incomplete")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return 0, errors.New("managed service process start tick is invalid")
	}
	return ticks, nil
}

func validateProcess(binding Binding) error {
	if binding.PID <= 1 || binding.ProcessGroupID <= 1 || binding.ProcessStartTicks == 0 {
		return errors.New("managed service process binding is invalid")
	}
	ticks, err := processStartTicks(binding.PID)
	if err != nil || ticks != binding.ProcessStartTicks {
		return errors.New("managed service process is no longer bound to this operation")
	}
	pgid, err := syscall.Getpgid(binding.PID)
	if err != nil || pgid != binding.ProcessGroupID {
		return errors.New("managed service process group no longer matches")
	}
	return nil
}

func probe(ctx context.Context, health domain.ManagedServiceHealthCheck) error {
	if health.Type == domain.ManagedServiceHealthProcess {
		return nil
	}
	timeout := time.Duration(health.TimeoutMillis) * time.Millisecond
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	address := net.JoinHostPort(health.Host, strconv.Itoa(health.Port))
	if health.Type == domain.ManagedServiceHealthTCP {
		connection, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", address)
		if err != nil {
			return fmt.Errorf("TCP health check failed: %w", err)
		}
		return connection.Close()
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+address+health.Path, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return fmt.Errorf("HTTP health check failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("HTTP health check returned %d", response.StatusCode)
	}
	return nil
}

func readBoundedAt(directoryFD int, name string, limit int64) ([]byte, int64, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 || stat.Size < 0 {
		return nil, 0, errors.New("managed service log is not a private owner-controlled file")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, 0, err
	}
	omitted := stat.Size - int64(len(content))
	if int64(len(content)) > limit {
		content = content[:limit]
		omitted = stat.Size - limit
	}
	return content, omitted, nil
}

func openServiceGeneration(dataDir, instanceID string, generation int, create bool) (int, error) {
	if !strings.HasPrefix(instanceID, "svcinst_") || strings.Contains(instanceID, "/") || generation < 1 || generation > 21 {
		return -1, errors.New("managed service runtime identity is invalid")
	}
	fd, err := openDataDirectory(dataDir)
	if err != nil {
		return -1, fmt.Errorf("open managed service data directory: %w", err)
	}
	for _, component := range []string{"service-runtime", instanceID, strconv.Itoa(generation)} {
		next, openErr := openPrivateDirectoryAt(fd, component, create)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, fmt.Errorf("open managed service runtime component %q: %w", component, openErr)
		}
		fd = next
	}
	return fd, nil
}

func openDataDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errors.New("managed service data directory is invalid")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.Trim(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	if err := validatePrivateDirectory(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func openPrivateDirectoryAt(parentFD int, component string, create bool) (int, error) {
	if component == "" || component == "." || component == ".." || strings.Contains(component, "/") {
		return -1, errors.New("managed service runtime directory name is invalid")
	}
	if create {
		if err := unix.Mkdirat(parentFD, component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return -1, err
		}
		if err := unix.Fsync(parentFD); err != nil {
			return -1, err
		}
	}
	fd, err := unix.Openat(parentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	if err := validatePrivateDirectory(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func validatePrivateDirectory(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Nlink < 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
		return errors.New("managed service runtime directory is not private and owner-controlled")
	}
	return nil
}

func parseServiceLogBinding(binding Binding) (string, int, error) {
	stdout := strings.Split(filepath.ToSlash(binding.StdoutPath), "/")
	stderr := strings.Split(filepath.ToSlash(binding.StderrPath), "/")
	if len(stdout) != 4 || len(stderr) != 4 || stdout[0] != "service-runtime" || stderr[0] != "service-runtime" ||
		stdout[1] != stderr[1] || stdout[2] != stderr[2] || stdout[3] != "stdout.log" || stderr[3] != "stderr.log" {
		return "", 0, errors.New("managed service log binding is invalid")
	}
	generation, err := strconv.Atoi(stdout[2])
	if err != nil {
		return "", 0, errors.New("managed service log generation is invalid")
	}
	return stdout[1], generation, nil
}

func exitDiagnostic(err error) string {
	if err == nil {
		return "managed service exited"
	}
	return "managed service exited: " + err.Error()
}
