package store

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestCheckDefinitionArgumentsUseSemanticJSONMirror(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	arguments := []string{"a<b", "x>y", "r&s", "<&>"}

	created, err := storage.CreateCheckDefinition(context.Background(), CreateCheckDefinitionCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		Name:                "semantic-json-arguments",
		Executable:          "/bin/true",
		Arguments:           arguments,
		WorkingDirectory:    ".",
		TimeoutMillis:       1_000,
		OutputByteLimit:     1_024,
		IdempotencyKey:      "create-semantic-json-arguments",
		CorrelationID:       "request-semantic-json-arguments",
	})
	if err != nil {
		t.Fatalf("CreateCheckDefinition() error = %v", err)
	}
	read, err := storage.CheckDefinition(context.Background(), workspace.ID, created.Value.ID)
	if err != nil {
		t.Fatalf("CheckDefinition() error = %v", err)
	}
	if !reflect.DeepEqual(read.Arguments, arguments) {
		t.Fatalf("CheckDefinition().Arguments = %#v, want exact semantic argv %#v", read.Arguments, arguments)
	}
	if read.ContentSHA256 == "" || read.ContentSHA256 != created.Value.ContentSHA256 {
		t.Fatalf("CheckDefinition().ContentSHA256 = %q, created = %q", read.ContentSHA256, created.Value.ContentSHA256)
	}
}

func TestGrantedCheckLaunchRevalidatesAuthorityBeforeFirstReceipt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *grantedCheckAuthorityFixture)
	}{
		{
			name: "revoked exact grant",
			mutate: func(t *testing.T, fixture *grantedCheckAuthorityFixture) {
				fixture.advance()
				if _, err := fixture.storage.RevokeCheckWatchGrant(context.Background(), RevokeCheckWatchGrantCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					CheckWatchGrantID:   fixture.grant.ID,
					ExpectedRevision:    fixture.grant.Revision,
					Reason:              "authority withdrawn before launch",
					IdempotencyKey:      "revoke-before-receipt",
					CorrelationID:       "request-revoke-before-receipt",
				}); err != nil {
					t.Fatalf("RevokeCheckWatchGrant() error = %v", err)
				}
			},
		},
		{
			name: "retired exact definition",
			mutate: func(t *testing.T, fixture *grantedCheckAuthorityFixture) {
				fixture.advance()
				if _, err := fixture.storage.RetireCheckDefinition(context.Background(), RetireCheckDefinitionCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					CheckDefinitionID:   fixture.definition.ID,
					ExpectedRevision:    fixture.definition.Revision,
					Reason:              "definition retired before launch",
					IdempotencyKey:      "retire-definition-before-receipt",
					CorrelationID:       "request-retire-definition-before-receipt",
				}); err != nil {
					t.Fatalf("RetireCheckDefinition() error = %v", err)
				}
			},
		},
		{
			name: "retired exact requirement",
			mutate: func(t *testing.T, fixture *grantedCheckAuthorityFixture) {
				fixture.advance()
				if _, err := fixture.storage.RetireTaskCheckRequirement(context.Background(), RetireTaskCheckRequirementCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					RequirementID:       fixture.requirement.ID,
					ExpectedRevision:    fixture.requirement.Revision,
					Reason:              "requirement retired before launch",
					IdempotencyKey:      "retire-requirement-before-receipt",
					CorrelationID:       "request-retire-requirement-before-receipt",
				}); err != nil {
					t.Fatalf("RetireTaskCheckRequirement() error = %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGrantedCheckAuthorityFixture(t)
			work := fixture.requestAndClaim(t)
			test.mutate(t, fixture)

			started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
			if err != nil {
				t.Fatalf("MarkCheckStarting(pre-receipt authority denial) error = %v", err)
			}
			if started.Run.Status != domain.CheckRunStarting || started.Run.RuntimeHandle != "" || started.LaunchReceipt == nil || started.LaunchReceipt.Launchable || started.LaunchReceipt.PreflightFailureCode == "" {
				t.Fatalf("MarkCheckStarting(pre-receipt authority denial) = %#v, want internally derived nonlaunchable receipt", started)
			}
			var receipts int
			if err := fixture.storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM check_launch_receipts WHERE check_run_id=?`, work.Run.ID).Scan(&receipts); err != nil {
				t.Fatalf("count launch receipts: %v", err)
			}
			if receipts != 1 {
				t.Fatalf("launch receipt count = %d, want one nonlaunchable denial receipt", receipts)
			}
			fixture.advance()
			terminalObservation := started.LaunchReceipt.Observation
			terminalObservation.ObservedAt = fixture.now.Format(time.RFC3339Nano)
			finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
				CheckRunID:          work.Run.ID,
				Outcome:             domain.CheckOutcomeStartFailed,
				DiagnosticCode:      started.LaunchReceipt.PreflightFailureCode,
				Diagnostic:          started.LaunchReceipt.PreflightFailureDiagnostic,
				TerminalObservation: terminalObservation,
				CorrelationID:       "worker-finish-authority-denied-check",
			})
			if err != nil {
				t.Fatalf("FinishCheckRun(authority denial) error = %v", err)
			}
			if finished.Run.Status != domain.CheckRunFinished || finished.Run.RuntimeHandle != "" || finished.Result == nil || finished.Result.Outcome != domain.CheckOutcomeStartFailed {
				t.Fatalf("FinishCheckRun(authority denial) = %#v, want visible start_failed with inconclusive mechanical evidence and no runtime", finished)
			}
			var evidenceClass, evidenceEffect string
			if err := fixture.storage.db.QueryRowContext(context.Background(), `SELECT class,effect FROM check_requirement_evidence WHERE check_result_id=?`, finished.Result.ID).Scan(&evidenceClass, &evidenceEffect); err != nil {
				t.Fatalf("read authority denial evidence: %v", err)
			}
			if evidenceClass != domain.EvidenceMechanicalCheck || evidenceEffect != domain.CheckEvidenceInconclusive {
				t.Fatalf("authority denial evidence = %q/%q, want mechanical_check/inconclusive", evidenceClass, evidenceEffect)
			}
		})
	}
}

func TestGrantedCheckReceiptPreservesExactRecoveryAfterAuthorityRetires(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	command := fixture.startCommand(work)
	started, err := fixture.storage.MarkCheckStarting(context.Background(), command)
	if err != nil {
		t.Fatalf("MarkCheckStarting() error = %v", err)
	}
	if started.Run.Status != domain.CheckRunStarting || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() detail = %#v, want starting with receipt", started)
	}
	receiptID := started.LaunchReceipt.ID

	fixture.advance()
	if _, err := fixture.storage.RevokeCheckWatchGrant(context.Background(), RevokeCheckWatchGrantCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		CheckWatchGrantID:   fixture.grant.ID,
		ExpectedRevision:    fixture.grant.Revision,
		Reason:              "authority withdrawn after launch receipt",
		IdempotencyKey:      "revoke-after-receipt",
		CorrelationID:       "request-revoke-after-receipt",
	}); err != nil {
		t.Fatalf("RevokeCheckWatchGrant() error = %v", err)
	}
	fixture.advance()
	if _, err := fixture.storage.RetireTaskCheckRequirement(context.Background(), RetireTaskCheckRequirementCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		RequirementID:       fixture.requirement.ID,
		ExpectedRevision:    fixture.requirement.Revision,
		Reason:              "requirement retired after launch receipt",
		IdempotencyKey:      "retire-requirement-after-receipt",
		CorrelationID:       "request-retire-requirement-after-receipt",
	}); err != nil {
		t.Fatalf("RetireTaskCheckRequirement() error = %v", err)
	}
	fixture.advance()
	if _, err := fixture.storage.RetireCheckDefinition(context.Background(), RetireCheckDefinitionCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		CheckDefinitionID:   fixture.definition.ID,
		ExpectedRevision:    fixture.definition.Revision,
		Reason:              "definition retired after launch receipt",
		IdempotencyKey:      "retire-definition-after-receipt",
		CorrelationID:       "request-retire-definition-after-receipt",
	}); err != nil {
		t.Fatalf("RetireCheckDefinition() error = %v", err)
	}

	// Expire the original worker lease to exercise the durable recovery path,
	// rather than merely replaying the receipt in the original transaction flow.
	fixture.now = fixture.now.Add(31 * time.Second)
	recovered, found, err := fixture.storage.ClaimCheckJob(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimCheckJob(recovery) error = %v", err)
	}
	if !found || recovered.Run.ID != work.Run.ID || recovered.LaunchReceipt == nil || recovered.LaunchReceipt.ID != receiptID {
		t.Fatalf("ClaimCheckJob(recovery) = found %t, work %#v; want exact receipted operation %s", found, recovered, receiptID)
	}
	replayed, err := fixture.storage.MarkCheckStarting(context.Background(), command)
	if err != nil {
		t.Fatalf("MarkCheckStarting(exact receipted replay after retirement) error = %v", err)
	}
	if replayed.LaunchReceipt == nil || replayed.LaunchReceipt.ID != receiptID || replayed.Run.Status != domain.CheckRunStarting {
		t.Fatalf("MarkCheckStarting(replay) = %#v, want original receipt %s", replayed, receiptID)
	}
	var receipts int
	if err := fixture.storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM check_launch_receipts WHERE check_run_id=?`, work.Run.ID).Scan(&receipts); err != nil {
		t.Fatalf("count launch receipts: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("launch receipt count = %d, want exactly one", receipts)
	}
}

func TestDerivedNonlaunchableCheckReceiptSupportsExactStoreReplay(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	command := fixture.startCommand(work)
	fixture.advance()
	if _, err := fixture.storage.RevokeCheckWatchGrant(context.Background(), RevokeCheckWatchGrantCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		CheckWatchGrantID:   fixture.grant.ID,
		ExpectedRevision:    fixture.grant.Revision,
		Reason:              "authority withdrawn before durable launch decision",
		IdempotencyKey:      "revoke-before-nonlaunchable-restart",
		CorrelationID:       "request-revoke-before-nonlaunchable-restart",
	}); err != nil {
		t.Fatalf("RevokeCheckWatchGrant() error = %v", err)
	}
	fixture.advance()
	command.Observation.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	started, err := fixture.storage.MarkCheckStarting(context.Background(), command)
	if err != nil || started.LaunchReceipt == nil || started.LaunchReceipt.Launchable || started.LaunchReceipt.PreflightFailureCode != domain.CheckPreflightAuthorityRevoked {
		t.Fatalf("MarkCheckStarting(derived denial) = %#v, %v; want authority_revoked receipt", started, err)
	}

	// A retry supplies the same caller-authored command. The denial fields were
	// derived internally, so their frozen replacement must not make that exact
	// semantic retry conflict with its own immutable receipt.
	replayed, err := fixture.storage.MarkCheckStarting(context.Background(), command)
	if err != nil {
		t.Fatalf("MarkCheckStarting(replay derived denial) error = %v", err)
	}
	if replayed.LaunchReceipt == nil || replayed.LaunchReceipt.ID != started.LaunchReceipt.ID || replayed.LaunchReceipt.Launchable || replayed.LaunchReceipt.PreflightFailureCode != domain.CheckPreflightAuthorityRevoked {
		t.Fatalf("MarkCheckStarting(replay derived denial) = %#v; want exact receipt %s", replayed, started.LaunchReceipt.ID)
	}
	assertCheckRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM check_launch_receipts WHERE check_run_id=?`, work.Run.ID)
}

func TestRecoverCheckJobLeasesPreservesReceiptedOperation(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil {
		t.Fatalf("MarkCheckStarting() error = %v", err)
	}
	if started.LaunchReceipt == nil || started.Job.Status != domain.CheckJobLeased {
		t.Fatalf("MarkCheckStarting() = %#v, want receipted leased operation", started)
	}

	if err := fixture.storage.RecoverCheckJobLeases(context.Background()); err != nil {
		t.Fatalf("RecoverCheckJobLeases() error = %v", err)
	}
	recovered, found, err := fixture.storage.ClaimCheckJob(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimCheckJob(recovered lease) error = %v", err)
	}
	if !found || recovered.Run.ID != work.Run.ID || recovered.Run.Status != domain.CheckRunStarting || recovered.LaunchReceipt == nil || recovered.LaunchReceipt.ID != started.LaunchReceipt.ID || recovered.Job.Attempts != work.Job.Attempts+1 {
		t.Fatalf("ClaimCheckJob(recovered lease) = found %t, work %#v; want same receipted starting operation with one additional attempt", found, recovered)
	}

	var availableAt, leaseExpires string
	var attempts int
	if err := fixture.storage.db.QueryRowContext(context.Background(), `SELECT available_at,lease_expires_at,attempts FROM check_jobs WHERE check_run_id=?`, work.Run.ID).Scan(&availableAt, &leaseExpires, &attempts); err != nil {
		t.Fatalf("read recovered job: %v", err)
	}
	if availableAt == "" || leaseExpires == "" || attempts != 2 {
		t.Fatalf("recovered job = available %q, lease %q, attempts %d; want immediately reclaimed second lease", availableAt, leaseExpires, attempts)
	}
}

type grantedCheckAuthorityFixture struct {
	storage     *Store
	now         time.Time
	workspace   domain.Workspace
	project     domain.Project
	agent       domain.AgentDefinition
	checkout    domain.Checkout
	task        domain.TaskDetail
	definition  domain.CheckDefinition
	requirement domain.TaskCheckRequirement
	grant       domain.CheckWatchGrant
	sourceRun   domain.Run
}

func newGrantedCheckAuthorityFixture(t *testing.T) *grantedCheckAuthorityFixture {
	t.Helper()
	fixture := &grantedCheckAuthorityFixture{now: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)}
	fixture.storage = openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return fixture.now }})
	fixture.workspace, fixture.project, fixture.agent, fixture.checkout, fixture.task = initializeRunTest(t, fixture.storage, "check-authority")
	// The baseline's project-policy seed is produced by SQLite's wall clock.
	// Align the controlled fixture clock once, before creating any leased or
	// expiring authority, so a long parallel package run cannot make a later
	// monotonicity adjustment jump past capabilities created earlier.
	var policyUpdatedAt string
	if err := fixture.storage.db.QueryRow(`SELECT updated_at FROM check_policies WHERE workspace_id=? AND project_id=?`, fixture.workspace.ID, fixture.project.ID).Scan(&policyUpdatedAt); err != nil {
		t.Fatal(err)
	}
	seededAt, err := time.Parse(time.RFC3339Nano, policyUpdatedAt)
	if err != nil {
		t.Fatalf("parse seeded check policy time: %v", err)
	}
	if !fixture.now.After(seededAt) {
		fixture.now = seededAt.Add(time.Second)
	}
	objective, err := fixture.storage.CreateObjective(context.Background(), CreateObjectiveCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		Title:               "owner check and repair objective",
		Budget:              domain.Budget{TokenLimit: 100000, CostCents: 5000, TimeSeconds: 43200},
		IdempotencyKey:      "create-check-authority-objective",
		CorrelationID:       "request-create-check-authority-objective",
	})
	if err != nil {
		t.Fatalf("CreateObjective(check authority) error = %v", err)
	}
	objectiveTask, err := fixture.storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		ObjectiveID:         objective.Value.ID,
		Title:               "owner-granted check source",
		Priority:            100,
		Budget:              domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600},
		IdempotencyKey:      "create-check-authority-objective-task",
		CorrelationID:       "request-create-check-authority-objective-task",
	})
	if err != nil {
		t.Fatalf("CreateTask(check authority objective) error = %v", err)
	}
	assignedObjectiveTask, err := fixture.storage.AssignTask(context.Background(), AssignTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		TaskID:              objectiveTask.Detail.Task.ID,
		AgentIdentifier:     fixture.agent.ID,
		LeaseSeconds:        300,
		ExpectedRevision:    objectiveTask.Detail.Task.Revision,
		IdempotencyKey:      "assign-check-authority-objective-task",
		CorrelationID:       "request-assign-check-authority-objective-task",
	})
	if err != nil {
		t.Fatalf("AssignTask(check authority objective) error = %v", err)
	}
	fixture.task = assignedObjectiveTask.Detail
	if err := os.MkdirAll(fixture.checkout.Path, 0o700); err != nil {
		t.Fatalf("MkdirAll(checkout) error = %v", err)
	}

	definition, err := fixture.storage.CreateCheckDefinition(context.Background(), CreateCheckDefinitionCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		Name:                "authority-bound-check",
		Executable:          "/bin/true",
		Arguments:           []string{"--version"},
		WorkingDirectory:    ".",
		TimeoutMillis:       1_000,
		OutputByteLimit:     1_024,
		IdempotencyKey:      "create-authority-bound-check",
		CorrelationID:       "request-create-authority-bound-check",
	})
	if err != nil {
		t.Fatalf("CreateCheckDefinition() error = %v", err)
	}
	fixture.definition = definition.Value
	requirement, err := fixture.storage.CreateTaskCheckRequirement(context.Background(), CreateTaskCheckRequirementCommand{
		WorkspaceIdentifier:       fixture.workspace.ID,
		TaskID:                    fixture.task.Task.ID,
		CriterionKey:              "authority-bound-check",
		Statement:                 "the exact owner-granted check must pass",
		CheckDefinitionID:         fixture.definition.ID,
		DefinitionContentRevision: fixture.definition.ContentRevision,
		ExpectedTaskRevision:      fixture.task.Task.Revision,
		IdempotencyKey:            "create-authority-bound-requirement",
		CorrelationID:             "request-create-authority-bound-requirement",
	})
	if err != nil {
		t.Fatalf("CreateTaskCheckRequirement() error = %v", err)
	}
	fixture.requirement = requirement.Value
	grant, err := fixture.storage.CreateCheckWatchGrant(context.Background(), CreateCheckWatchGrantCommand{
		WorkspaceIdentifier:   fixture.workspace.ID,
		ProjectIdentifier:     fixture.project.ID,
		AgentIdentifier:       fixture.agent.ID,
		ExpectedAgentRevision: fixture.agent.Revision,
		Operations:            []string{domain.CheckWatchOperationInspect, domain.CheckWatchOperationProposeRepair, domain.CheckWatchOperationRun},
		Definitions:           []CheckDefinitionRevision{{DefinitionID: fixture.definition.ID, ContentRevision: fixture.definition.ContentRevision}},
		MaxPending:            4,
		MaxInFlight:           2,
		ExpiresAt:             fixture.now.Add(time.Hour).Format(time.RFC3339Nano),
		IdempotencyKey:        "create-authority-bound-grant",
		CorrelationID:         "request-create-authority-bound-grant",
	})
	if err != nil {
		t.Fatalf("CreateCheckWatchGrant() error = %v", err)
	}
	fixture.grant = grant.Value
	source, err := fixture.storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		TaskID:              fixture.task.Task.ID,
		CheckoutIdentifier:  fixture.checkout.ID,
		Runtime:             "fake",
		Provider:            "fake",
		Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema,
			Name:   "authority-bound-watcher",
			Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}},
		},
		ExpectedTaskRevision:            fixture.task.Task.Revision,
		CapabilityTTL:                   time.Hour,
		CheckWatchGrantID:               fixture.grant.ID,
		ExpectedCheckWatchGrantRevision: fixture.grant.Revision,
		IdempotencyKey:                  "create-authority-bound-source-run",
		CorrelationID:                   "request-create-authority-bound-source-run",
	})
	if err != nil {
		t.Fatalf("CreateRun(check watcher) error = %v", err)
	}
	fixture.sourceRun = source.Detail.Run
	fixture.advance()
	startedSource, err := fixture.storage.MarkRunStarting(context.Background(), fixture.sourceRun.ID, "worker-start-authority-bound-source")
	if err != nil {
		t.Fatalf("MarkRunStarting(check watcher) error = %v", err)
	}
	fixture.sourceRun = startedSource
	return fixture
}

func (fixture *grantedCheckAuthorityFixture) advance() {
	fixture.now = fixture.now.Add(time.Second)
}

func (fixture *grantedCheckAuthorityFixture) requestAndClaim(t *testing.T) CheckWork {
	t.Helper()
	requested, err := fixture.storage.RunGrantedCheck(context.Background(), RequestGrantedCheckRunCommand{
		SourceRunID:           fixture.sourceRun.ID,
		CheckWatchGrantID:     fixture.grant.ID,
		ExpectedGrantRevision: fixture.grant.Revision,
		RequirementID:         fixture.requirement.ID,
		IdempotencyKey:        "request-authority-bound-check",
		CorrelationID:         "request-authority-bound-check",
	})
	if err != nil {
		t.Fatalf("RunGrantedCheck() error = %v", err)
	}
	work, found, err := fixture.storage.ClaimCheckJob(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("ClaimCheckJob() error = %v", err)
	}
	if !found || work.Run.ID != requested.Value.ID || work.Job.Status != domain.CheckJobLeased {
		t.Fatalf("ClaimCheckJob() = found %t, work %#v; want leased run %s", found, work, requested.Value.ID)
	}
	return work
}

func (fixture *grantedCheckAuthorityFixture) startCommand(work CheckWork) MarkCheckStartingCommand {
	return MarkCheckStartingCommand{
		CheckRunID:                work.Run.ID,
		OperationID:               work.Run.ID,
		EffectiveSpecSHA256:       strings.Repeat("a", 64),
		EffectiveWorkingDirectory: fixture.checkout.Path,
		Launchable:                true,
		Observation: domain.CheckGitObservation{
			Available:    true,
			RepositoryID: work.Run.RepositoryID,
			ObjectFormat: work.Run.RepositoryObjectFormat,
			CheckoutID:   work.Run.CheckoutID,
			Branch:       fixture.checkout.Branch,
			HeadCommit:   fixture.checkout.HeadCommit,
			DirtyPaths:   []string{},
			ObservedAt:   fixture.now.Format(time.RFC3339Nano),
		},
		CorrelationID: "worker-start-authority-bound-check",
	}
}
