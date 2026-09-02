//go:build linux

package service

import (
	"strings"
	"testing"
)

func TestSystemdUnitQuotesDaemonConfiguration(t *testing.T) {
	unit := systemdUnit(Config{
		Executable: `/home/owner/Crewfold Release/crewfold`,
		DataDir:    `/home/owner/.local/state/crewfold`,
		Endpoint:   `/run/user/1000/crewfold/crewfold.sock`,
	})
	for _, expected := range []string{
		`ExecStart="/home/owner/Crewfold Release/crewfold"`,
		`--data-dir "/home/owner/.local/state/crewfold"`,
		`--socket "/run/user/1000/crewfold/crewfold.sock"`,
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("unit does not contain %q:\n%s", expected, unit)
		}
	}
}
