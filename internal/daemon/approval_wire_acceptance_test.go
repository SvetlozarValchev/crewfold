package daemon

import (
	"context"
	"os/exec"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestM19RealDaemonApprovalDecisionResultsPassStrictWireValidation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}
	for _, test := range []struct {
		name         string
		allow        bool
		approvalWant string
		actionWant   string
	}{
		{name: "allow", allow: true, approvalWant: domain.ApprovalConsumed, actionWant: domain.SupervisorActionApplied},
		{name: "deny", approvalWant: domain.ApprovalDenied, actionWant: domain.SupervisorActionDismissed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixtureRoot := t.TempDir()
			createGitFixture(t, fixtureRoot)
			config := testConfig(t)
			config.DisableSupervisor = true
			config.DisableClaimWatcher = true
			config.DisableCheckWatcher = true
			running := startTestServer(t, config)
			client := localapi.NewClient(config.SocketPath)
			project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
			task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "approval wire "+test.name)
			started, err := client.RunStart(context.Background(), localapi.RunStartParams{
				Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake",
				Scenario: domain.FakeScenario{
					Schema: execution.FakeScenarioSchema, Name: "approval-wire-" + test.name,
					Steps: []domain.FakeStep{
						{Kind: domain.ObservationBlocked, Message: "owner decision required"},
						{Kind: domain.ObservationCompletion, Message: "decision applied", Handoff: "owner decision recorded"},
					},
				},
				ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "approval-wire-start-" + test.name,
			})
			if err != nil {
				t.Fatalf("RunStart() error = %v", err)
			}
			blocked := waitForRunStatus(t, client, started.Detail.Run.ID, domain.RunBlocked)
			if _, err := client.SupervisorPolicyConfigure(context.Background(), localapi.SupervisorPolicyConfigureParams{
				Workspace: "personal", Enabled: true, AutoSchedule: true, ExpectedRevision: 1,
				IdempotencyKey: "approval-wire-policy-" + test.name,
				Limits: domain.SupervisorLimits{
					MaxActiveRuns: 4, MaxStartingRuns: 2, DefaultProjectConcurrency: 2, DefaultProviderConcurrency: 2,
					ProjectConcurrency: map[string]int{}, ProviderConcurrency: map[string]int{},
				},
			}); err != nil {
				t.Fatalf("SupervisorPolicyConfigure() error = %v", err)
			}
			scan, err := client.SupervisorRun(context.Background(), localapi.SupervisorRunParams{
				Workspace: "personal", Limit: 100, IdempotencyKey: "approval-wire-scan-" + test.name,
			})
			if err != nil {
				t.Fatalf("SupervisorRun() error = %v", err)
			}
			if len(scan.Actions) != 1 || scan.Actions[0].RunID != blocked.Detail.Run.ID ||
				scan.Actions[0].Status != domain.SupervisorActionAwaitingApproval || scan.Actions[0].ApprovalID == "" {
				t.Fatalf("blocked supervisor scan = %#v", scan)
			}
			action := scan.Actions[0]
			shown, err := client.ApprovalInspect(context.Background(), "personal", action.ApprovalID)
			if err != nil {
				t.Fatalf("ApprovalInspect() error = %v", err)
			}
			note := "owner reviewed exact " + test.name
			params := localapi.ApprovalDecisionParams{
				Workspace: "personal", Approval: shown.Approval.ID, ExpectedRevision: shown.Approval.Revision,
				DecisionNote: note, IdempotencyKey: "approval-wire-decision-" + test.name,
			}
			var decided localapi.ApprovalMutationResult
			if test.allow {
				decided, err = client.ApprovalAllow(context.Background(), params)
			} else {
				decided, err = client.ApprovalDeny(context.Background(), params)
			}
			if err != nil {
				t.Fatalf("strict approval.%s real response error = %v", test.name, err)
			}
			if decided.Approval.Status != test.approvalWant || decided.Action.Status != test.actionWant ||
				decided.Approval.DecisionEventSequence < 1 || decided.Approval.DecisionNote != note || decided.Action.Decision != note ||
				decided.Approval.ActionID != decided.Action.ID || decided.Action.ApprovalID != decided.Approval.ID ||
				decided.Approval.ProjectID != project.Project.ID || decided.Action.ProjectID != project.Project.ID {
				t.Fatalf("strict approval.%s governed result = %#v", test.name, decided)
			}

			if _, err := client.Stop(context.Background()); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			if err := running.wait(); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}
