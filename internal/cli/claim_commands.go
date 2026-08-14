package cli

import (
	"context"
	"fmt"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func (a *App) runClaim(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("claim requires a subcommand", "run 'crewfold help claim' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, claimHelp)
		return ExitOK
	}
	switch args[0] {
	case "add":
		return a.runClaimAdd(ctx, mode, args[1:])
	case "list":
		return a.runClaimList(ctx, mode, args[1:])
	case "release":
		return a.runClaimRelease(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown claim command %q", args[0]), "run 'crewfold help claim' for usage"))
	}
}

func (a *App) runClaimAdd(ctx context.Context, mode outputMode, args []string) int {
	task, optionArgs, failure := requiredLeadingArgument(args, "task ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "project", "checkout", "write", "component", "operation", "mode", "policy", "lease", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	project, failure := requiredOption(options, "project")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	kind, target := "", ""
	for _, candidate := range []struct{ option, kind string }{{"write", domain.ClaimKindPath}, {"component", domain.ClaimKindComponent}, {"operation", domain.ClaimKindOperation}} {
		if value := options[candidate.option]; value != "" {
			if kind != "" {
				return a.writeFailure(mode, usageFailure("claim add accepts exactly one of --write, --component, or --operation", "run 'crewfold help claim' for usage"))
			}
			kind, target = candidate.kind, value
		}
	}
	if kind == "" {
		return a.writeFailure(mode, usageFailure("claim add requires one of --write, --component, or --operation", "run 'crewfold help claim' for usage"))
	}
	leaseText, failure := requiredOption(options, "lease")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	lease, err := time.ParseDuration(leaseText)
	if err != nil || lease < time.Second || lease > 30*24*time.Hour {
		return a.writeFailure(mode, usageFailure("--lease must be a duration from 1s through 720h, such as 1h", "run 'crewfold help claim' for usage"))
	}
	result, err := a.newClient(socket).ClaimAdd(ctx, localapi.ClaimAddParams{
		Workspace: workspace, Project: project, Task: task, Checkout: options["checkout"], Kind: kind, Target: target,
		Mode: options["mode"], ConflictPolicy: options["policy"], LeaseSeconds: int64(lease / time.Second), IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "add claim", err)
	}
	return a.writeClaimMutation(mode, result)
}

func (a *App) runClaimList(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "workspace", "project", "status", "cursor", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 200)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ClaimList(ctx, localapi.ClaimListParams{Workspace: workspace, Project: options["project"], Status: options["status"], PageParams: localapi.PageParams{Cursor: options["cursor"], Limit: int(limit)}})
	if err != nil {
		return a.writeClientFailure(mode, "list claims", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write claim list", err))
		}
	} else {
		for _, claim := range result.Claims {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\t%s\t%s\tlease=%s\n", claim.ID, claim.TaskID, claim.Kind, claim.Mode, claim.Status, claim.Target, claim.LeaseExpiresAt)
		}
		writePageMetadata(a.stdout, result.PageResult, len(result.Claims))
	}
	return ExitOK
}

func (a *App) runClaimRelease(ctx context.Context, mode outputMode, args []string) int {
	claim, optionArgs, failure := requiredLeadingArgument(args, "claim ID")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(optionArgs, "workspace", "expected-revision", "socket", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	revision, failure := requiredInt64Option(options, "expected-revision", 1, 1<<62)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).ClaimRelease(ctx, localapi.ClaimReleaseParams{Workspace: workspace, Claim: claim, ExpectedRevision: revision, IdempotencyKey: options["idempotency-key"]})
	if err != nil {
		return a.writeClientFailure(mode, "release claim", err)
	}
	return a.writeClaimMutation(mode, result)
}

func (a *App) writeClaimMutation(mode outputMode, result localapi.ClaimMutationResult) int {
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write claim mutation", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "claim: %s\nstatus: %s\nscope: %s %s\nlease_expires_at: %s\n", result.Claim.ID, result.Claim.Status, result.Claim.Kind, result.Claim.Target, result.Claim.LeaseExpiresAt)
		for _, overlap := range result.Overlaps {
			fmt.Fprintf(a.stdout, "overlap: %s severity=%s policy=%s witness=%s\n", overlap.ID, overlap.Severity, overlap.PolicyResponse, overlap.Witness)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(a.stdout, "warning: %s\n", warning)
		}
	}
	return ExitOK
}

func (a *App) runOverlap(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("overlap requires a subcommand", "run 'crewfold help overlap' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, overlapHelp)
		return ExitOK
	}
	switch args[0] {
	case "list":
		options, failure := parseOptions(args[1:], "workspace", "project", "status", "cursor", "limit", "socket")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		workspace, socket, failure := requiredWorkspaceSocket(options)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		limit, failure := optionalIntOption(options, "limit", 1, 200)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		result, err := a.newClient(socket).OverlapList(ctx, localapi.OverlapListParams{Workspace: workspace, Project: options["project"], Status: options["status"], PageParams: localapi.PageParams{Cursor: options["cursor"], Limit: int(limit)}})
		if err != nil {
			return a.writeClientFailure(mode, "list overlaps", err)
		}
		if mode == outputJSON {
			if err := writeJSON(a.stdout, result); err != nil {
				return a.writeFailure(outputText, internalFailure("write overlap list", err))
			}
		} else {
			for _, overlap := range result.Overlaps {
				fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\t%s\n", overlap.ID, overlap.Status, overlap.Severity, overlap.PolicyResponse, overlap.Witness)
			}
			writePageMetadata(a.stdout, result.PageResult, len(result.Overlaps))
		}
		return ExitOK
	case "inspect":
		overlap, optionArgs, failure := requiredLeadingArgument(args[1:], "overlap ID")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		options, failure := parseOptions(optionArgs, "workspace", "socket")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		workspace, socket, failure := requiredWorkspaceSocket(options)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		result, err := a.newClient(socket).OverlapInspect(ctx, workspace, overlap)
		if err != nil {
			return a.writeClientFailure(mode, "inspect overlap", err)
		}
		if mode == outputJSON {
			if err := writeJSON(a.stdout, result); err != nil {
				return a.writeFailure(outputText, internalFailure("write overlap", err))
			}
		} else {
			fmt.Fprintf(a.stdout, "overlap: %s\nstatus: %s\nseverity: %s\npolicy: %s\nwitness: %s\n", result.Overlap.ID, result.Overlap.Status, result.Overlap.Severity, result.Overlap.PolicyResponse, result.Overlap.Witness)
			for _, explanation := range result.Overlap.Explanation {
				fmt.Fprintf(a.stdout, "- %s\n", explanation)
			}
		}
		return ExitOK
	case "scan":
		options, failure := parseOptions(args[1:], "workspace", "project", "socket")
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		workspace, socket, failure := requiredWorkspaceSocket(options)
		if failure != nil {
			return a.writeFailure(mode, *failure)
		}
		result, err := a.newClient(socket).OverlapScan(ctx, workspace, options["project"])
		if err != nil {
			return a.writeClientFailure(mode, "scan claimed checkouts", err)
		}
		if mode == outputJSON {
			if err := writeJSON(a.stdout, result); err != nil {
				return a.writeFailure(outputText, internalFailure("write overlap scan", err))
			}
		} else {
			for _, scan := range result.Scans {
				fmt.Fprintf(a.stdout, "%s\tdirty=%d\tdrift_opened=%d\tdrift_resolved=%d\tgap=%t\n", scan.CheckoutID, len(scan.DirtyPaths), scan.DriftsOpened, scan.DriftsResolved, scan.ObservationGap)
			}
			for _, issue := range result.Issues {
				fmt.Fprintf(a.stdout, "warning: %s\n", issue)
			}
		}
		return ExitOK
	default:
		return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown overlap command %q", args[0]), "run 'crewfold help overlap' for usage"))
	}
}

func (a *App) runDrift(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, driftHelp)
		return ExitOK
	}
	if len(args) == 0 || args[0] != "list" {
		return a.writeFailure(mode, usageFailure("drift requires the list subcommand", "run 'crewfold help drift' for usage"))
	}
	options, failure := parseOptions(args[1:], "workspace", "project", "status", "cursor", "limit", "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	workspace, socket, failure := requiredWorkspaceSocket(options)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	limit, failure := optionalIntOption(options, "limit", 1, 200)
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socket).DriftList(ctx, localapi.DriftListParams{Workspace: workspace, Project: options["project"], Status: options["status"], PageParams: localapi.PageParams{Cursor: options["cursor"], Limit: int(limit)}})
	if err != nil {
		return a.writeClientFailure(mode, "list claim drift", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write drift list", err))
		}
	} else {
		for _, drift := range result.Drifts {
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\tgap=%t\n", drift.ID, drift.Status, drift.TaskID, drift.Path, drift.ObservationGap)
		}
		writePageMetadata(a.stdout, result.PageResult, len(result.Drifts))
	}
	return ExitOK
}

const claimHelp = `Usage:
  crewfold claim add <task-id> --workspace <scope> --project <project> --write <glob> --lease <duration> --socket <path> [options]
  crewfold claim add <task-id> --workspace <scope> --project <project> --component <name> --lease <duration> --socket <path> [options]
  crewfold claim add <task-id> --workspace <scope> --project <project> --operation <name> --lease <duration> --socket <path> [options]
  crewfold claim list --workspace <scope> [--project <project>] [--status active|expired|released] [--cursor <cursor>] [--limit <1..200>] --socket <path>
  crewfold claim release <claim-id> --workspace <scope> --expected-revision <n> --socket <path>

Path claims accept repository-relative literals, *, ?, and whole-segment **.
Use --checkout when a project has multiple writable checkouts. Modes are
exclusive, shared, or advisory. Policies are notify, deny_new,
pause_scheduling, or request_resolution. Claims coordinate declared intent;
they do not provide filesystem isolation.
`

const overlapHelp = `Usage:
  crewfold overlap list --workspace <scope> [--project <project>] [--status open|resolved] [--cursor <cursor>] [--limit <1..200>] --socket <path>
  crewfold overlap inspect <overlap-id> --workspace <scope> --socket <path>
  crewfold overlap scan --workspace <scope> [--project <project>] --socket <path>

Overlaps use a deterministic scope/mode/policy matrix and include a concrete
intersection witness. Scan performs a bounded read-only Git observation.
`

const driftHelp = `Usage:
  crewfold drift list --workspace <scope> [--project <project>] [--status open|resolved] [--cursor <cursor>] [--limit <1..200>] --socket <path>

Drift records observed dirty paths outside a task's declared path claims. It is
evidence, not an automatic rewrite of the task's declaration.
`
