package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const providerMaximumProbeOutput = 1024 * 1024

type ProviderCommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type ProviderCommandRunner interface {
	Run(context.Context, string, []string, map[string]string) (ProviderCommandResult, error)
}

type ProviderExecRunner struct{}

func (ProviderExecRunner) Run(ctx context.Context, executable string, arguments []string, environment map[string]string) (ProviderCommandResult, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = mergedProviderEnvironment(os.Environ(), environment)
	stdout := &boundedCommandBuffer{limit: providerMaximumProbeOutput}
	stderr := &boundedCommandBuffer{limit: providerMaximumProbeOutput}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	result := ProviderCommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

type boundedCommandBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *boundedCommandBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.buffer.Write(value)
	}
	return written, nil
}

func (buffer *boundedCommandBuffer) Bytes() []byte {
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func mergedProviderEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

func providerCommandDiagnostic(result ProviderCommandResult, fallback string) string {
	value := strings.TrimSpace(string(result.Stderr))
	if value == "" {
		value = strings.TrimSpace(string(result.Stdout))
	}
	if value == "" {
		value = fallback
	}
	return boundedProviderDiagnostic(value)
}

func boundedProviderDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 4096 {
		value = value[:4096]
	}
	return value
}
