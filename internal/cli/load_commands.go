package cli

import (
	"context"
	"fmt"

	"crewfold/internal/loadtest"
)

func (a *App) runTest(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("test requires a subcommand", "run 'crewfold help test' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, testHelp)
		return ExitOK
	}
	if args[0] != "load" {
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_test_command",
			message:  fmt.Sprintf("unknown test command %q", args[0]),
			hint:     "run 'crewfold help test' for usage",
		})
	}
	options, failure := parseOptions(args[1:], "profile")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	profile, failure := requiredOption(options, "profile")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if profile != loadtest.Personal100Profile {
		return a.writeFailure(mode, usageFailure(
			fmt.Sprintf("unsupported load profile %q", profile),
			"use --profile personal-100",
		))
	}

	report, runErr := a.runPersonalLoad(ctx)
	if mode == outputJSON {
		if err := writeJSON(a.stdout, report); err != nil {
			return a.writeFailure(outputText, internalFailure("write personal load report", err))
		}
	} else {
		writePersonalLoadText(a, report)
	}
	if runErr != nil {
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitFailure,
			code:     "personal_load_failed",
			message:  runErr.Error(),
			hint:     "inspect the emitted report and rerun on an idle supported Linux host",
		})
	}
	if report.Status != "ok" {
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitFailure,
			code:     "personal_load_failed",
			message:  "personal-100 completed without an ok report",
			hint:     "inspect the emitted assertions and resource measurements",
		})
	}
	return ExitOK
}

func writePersonalLoadText(a *App, report loadtest.Report) {
	fmt.Fprintln(a.stdout, "Crewfold personal-100 load")
	fmt.Fprintf(a.stdout, "workspaces: %d\nprojects: %d\nagents: %d\nobjectives: %d\ntasks: %d\nknown_events: %d\nnoisy_project_events: %d\n",
		report.Counts.Workspaces, report.Counts.Projects, report.Counts.Agents,
		report.Counts.Objectives, report.Counts.Tasks, report.Counts.KnownEvents,
		report.Counts.NoisyProjectEvents)
	for _, assertion := range report.Assertions {
		status := "failed"
		if assertion.Passed {
			status = "ok"
		}
		fmt.Fprintf(a.stdout, "  %-34s %s (%d/%d %s)\n", assertion.Name+":", status, assertion.Actual, assertion.Limit, assertion.Unit)
	}
	fmt.Fprintf(a.stdout, "logical_sha256: %s\nstatus: %s\n", report.LogicalSHA256, report.Status)
}

const testHelp = `Usage:
  crewfold test load --profile personal-100

Build and verify the exact isolated provider-free personal-scale fixture. The
command accepts no socket, data directory, checkout, provider, or credential
path and always removes its owned temporary directory.
`
