//go:build !linux

package servicehost

import (
	"context"
	"errors"
	"time"

	"crewfold/internal/domain"
)

type Binding struct {
	PID, ProcessGroupID    int
	ProcessStartTicks      uint64
	StdoutPath, StderrPath string
}
type Snapshot struct {
	Running, Tracked, ExitKnown bool
	ExitCode                    int
	Diagnostic, Health          string
}
type Host struct{}

func New(string) *Host { return &Host{} }
func (*Host) Start(context.Context, string, int, string, domain.ManagedServiceDefinition) (Binding, error) {
	return Binding{}, errors.New("managed services require Linux")
}
func (*Host) Inspect(context.Context, string, Binding, domain.ManagedServiceHealthCheck) Snapshot {
	return Snapshot{Diagnostic: "managed services require Linux", Health: domain.ManagedServiceHealthUnknown}
}
func (*Host) Stop(context.Context, string, Binding, time.Duration) (bool, error) {
	return false, errors.New("managed services require Linux")
}
func (*Host) ReadLogs(string, Binding, int64) ([]byte, []byte, int64, int64, error) {
	return nil, nil, 0, 0, errors.New("managed services require Linux")
}
func (*Host) Forget(string) {}
func (*Host) RemoveRuntime(string) error {
	return errors.New("managed services require Linux")
}
