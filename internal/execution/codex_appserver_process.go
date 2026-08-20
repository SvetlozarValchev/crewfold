package execution

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const maximumCodexAppServerStderrBytes = 64 * 1024

type CodexAppServerProcessOptions struct {
	Executable string
	CodexHome  string
}

// StartCodexAppServer starts the installed subscription-backed Codex host over
// its structured stdio protocol. No shell is involved and stderr is bounded so
// provider diagnostics cannot become an unbounded daemon allocation.
func StartCodexAppServer(options CodexAppServerProcessOptions) (CodexAppServerTransport, error) {
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		executable = "codex"
	}
	command := exec.Command(executable, "app-server", "--listen", "stdio://")
	return startCodexAppServerCommand(command, options.CodexHome)
}

func startCodexAppServerCommand(command *exec.Cmd, codexHome string) (CodexAppServerTransport, error) {
	command.Env = os.Environ()
	if codexHome := strings.TrimSpace(codexHome); codexHome != "" {
		command.Env = append(command.Env, "CODEX_HOME="+codexHome)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	stderr := &boundedAppServerBuffer{maximum: maximumCodexAppServerStderrBytes}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	process := &codexAppServerProcess{command: command, stdin: stdin, stdout: stdout, stderr: stderr, waited: make(chan struct{})}
	go func() {
		process.waitErr = command.Wait()
		close(process.waited)
	}()
	return process, nil
}

type codexAppServerProcess struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    *boundedAppServerBuffer
	waited    chan struct{}
	waitErr   error
	closeOnce sync.Once
	closeErr  error
}

func (process *codexAppServerProcess) Read(data []byte) (int, error) {
	return process.stdout.Read(data)
}

func (process *codexAppServerProcess) Write(data []byte) (int, error) {
	return process.stdin.Write(data)
}

func (process *codexAppServerProcess) Close() error {
	process.closeOnce.Do(func() {
		_ = process.stdin.Close()
		select {
		case <-process.waited:
		case <-time.After(2 * time.Second):
			if process.command.Process != nil {
				_ = process.command.Process.Kill()
			}
			<-process.waited
		}
		_ = process.stdout.Close()
		if process.waitErr != nil && !expectedCodexAppServerExit(process.waitErr) {
			process.closeErr = fmt.Errorf("Codex app-server exited: %w: %s", process.waitErr, process.stderr.String())
		}
	})
	return process.closeErr
}

func expectedCodexAppServerExit(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && (exitError.ExitCode() == -1 || exitError.ExitCode() == 0)
}

type boundedAppServerBuffer struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	maximum int
}

func (buffer *boundedAppServerBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(data)
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.buffer.Write(data)
	}
	return original, nil
}

func (buffer *boundedAppServerBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
