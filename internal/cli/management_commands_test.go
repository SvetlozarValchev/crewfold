package cli

import (
	"strings"
	"testing"
)

func TestSupervisorConcurrencyMapCapsAtOneHundred(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"zero":     `{"project":0}`,
		"over cap": `{"project":101}`,
	} {
		if _, failure := concurrencyMapOption(map[string]string{"limits": value}, "limits"); failure == nil {
			t.Errorf("%s concurrency value unexpectedly accepted", name)
		}
	}
	got, failure := concurrencyMapOption(map[string]string{"limits": `{"project":100}`}, "limits")
	if failure != nil || got["project"] != 100 {
		t.Fatalf("maximum concurrency = %#v, %v", got, failure)
	}
}

func TestManagerHelpExplainsExactAuthorityAndNonCircularSetup(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := newTestApp()
	if exit := app.Run([]string{"help", "manager"}); exit != ExitOK {
		t.Fatalf("help manager exit = %d, stderr = %s", exit, stderr.String())
	}
	output := stdout.String()
	for _, fragment := range []string{
		"never an agent role label",
		"target launch profiles first",
		"assign the planning task",
		"grant allowlisting the targets",
		"exactly one current compatible tuple",
	} {
		if !strings.Contains(output, fragment) {
			t.Errorf("manager help omits %q:\n%s", fragment, output)
		}
	}
	if strings.Contains(output, "role ==") || strings.Contains(output, "manager role") {
		t.Fatalf("manager help implies role-name authority:\n%s", output)
	}
}

func TestSupervisorPolicyCLIRejectsConcurrencyAboveOneHundred(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp()
	exit := app.Run([]string{
		"supervisor", "policy", "update", "--workspace", "personal", "--enabled", "true", "--auto-schedule", "true",
		"--auto-retry-limit", "0", "--retry-cooldown-seconds", "0", "--max-active-runs", "101",
		"--max-starting-runs", "1", "--default-project-concurrency", "1", "--default-provider-concurrency", "1",
		"--socket", "/tmp/unused.sock",
	})
	if exit != ExitUsage || !strings.Contains(stderr.String(), "1 to 100") {
		t.Fatalf("supervisor over-cap exit = %d, stderr = %s", exit, stderr.String())
	}
}
