package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestRawSQLCannotMutateOrDetachCheckAuthorityAndEvidence(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	route, err := fixture.storage.CreateCheckRoute(context.Background(), CreateCheckRouteCommand{
		WorkspaceIdentifier:       fixture.workspace.ID,
		ProjectIdentifier:         fixture.project.ID,
		CheckDefinitionID:         fixture.definition.ID,
		DefinitionContentRevision: fixture.definition.ContentRevision,
		Trigger:                   domain.CheckRoutePass,
		Duty:                      domain.CheckDutyEvidenceReview,
		AgentIdentifier:           fixture.agent.ID,
		ExpectedAgentRevision:     fixture.agent.Revision,
		IdempotencyKey:            "raw-sql-check-route",
		CorrelationID:             "request-raw-sql-check-route",
	})
	if err != nil {
		t.Fatalf("CreateCheckRoute() error = %v", err)
	}
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "raw-sql-runtime-binding"); err != nil {
		t.Fatalf("RecordCheckRuntimeBinding() error = %v", err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "raw-sql-running"); err != nil {
		t.Fatalf("MarkCheckRunning() error = %v", err)
	}
	fixture.advance()
	exitCode := 0
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID:          work.Run.ID,
		Outcome:             domain.CheckOutcomePassed,
		ExitCode:            &exitCode,
		TerminalObservation: terminal,
		CorrelationID:       "raw-sql-finish",
	})
	if err != nil || finished.Result == nil || finished.CurrentFreshness == nil {
		t.Fatalf("FinishCheckRun() = %#v, %v", finished, err)
	}
	now := fixture.now.AddDate(0, 0, 1).Format(time.RFC3339Nano)

	attacks := []struct {
		name      string
		statement string
		arguments []any
	}{
		{"definition argv delete", `DELETE FROM check_definition_arguments WHERE definition_id=?`, []any{fixture.definition.ID}},
		{"definition lifecycle forge", `UPDATE check_definitions SET status='retired',revision=revision+1,updated_at=? WHERE id=?`, []any{now, fixture.definition.ID}},
		{"requirement lifecycle forge", `UPDATE task_check_requirements SET status='retired',revision=revision+1,updated_at=? WHERE id=?`, []any{now, fixture.requirement.ID}},
		{"grant operation delete", `DELETE FROM check_watch_grant_operations WHERE grant_id=?`, []any{fixture.grant.ID}},
		{"grant definition delete", `DELETE FROM check_watch_grant_definitions WHERE grant_id=?`, []any{fixture.grant.ID}},
		{"grant lifecycle forge", `UPDATE check_watch_grants SET status='revoked',revision=revision+1,updated_at=? WHERE id=?`, []any{now, fixture.grant.ID}},
		{"policy escalation", `UPDATE check_policies SET max_open_repair_proposals=2,revision=revision+1,updated_at=? WHERE project_id=?`, []any{now, fixture.project.ID}},
		{"route lifecycle forge", `UPDATE check_routes SET status='retired',revision=revision+1,updated_at=? WHERE id=?`, []any{now, route.Value.ID}},
		{"run detach", `DELETE FROM check_runs WHERE id=?`, []any{work.Run.ID}},
		{"job detach", `DELETE FROM check_jobs WHERE check_run_id=?`, []any{work.Run.ID}},
		{"launch receipt rewrite", `UPDATE check_launch_receipts SET effective_spec_sha256=? WHERE check_run_id=?`, []any{differentCheckSHA256(started.LaunchReceipt.EffectiveSpecSHA256), work.Run.ID}},
		{"result rewrite", `UPDATE check_results SET outcome='unknown',exit_code=NULL WHERE id=?`, []any{finished.Result.ID}},
		{"freshness detach", `DELETE FROM check_result_freshness WHERE check_result_id=?`, []any{finished.Result.ID}},
		{"evidence promotion", `UPDATE check_requirement_evidence SET class='policy_acceptance',effect='supports' WHERE check_result_id=?`, []any{finished.Result.ID}},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			if _, err := fixture.storage.db.ExecContext(context.Background(), attack.statement, attack.arguments...); err == nil {
				t.Fatalf("raw SQL attack succeeded: %s", attack.statement)
			}
		})
	}

	read, err := fixture.storage.CheckRunDetail(context.Background(), fixture.workspace.ID, work.Run.ID)
	if err != nil || read.Result == nil || read.Result.Outcome != domain.CheckOutcomePassed || read.CurrentFreshness == nil || read.CurrentFreshness.Status != domain.CheckFreshnessFresh || len(read.Evidence.MechanicalCheck) != 1 {
		t.Fatalf("CheckRunDetail(after raw attacks) = %#v, %v", read, err)
	}
	if read.Evidence.MechanicalCheck[0].Class != domain.EvidenceMechanicalCheck || read.Evidence.MechanicalCheck[0].Effect != domain.CheckEvidenceSupports {
		t.Fatalf("mechanical evidence after raw attacks = %#v", read.Evidence.MechanicalCheck)
	}
}

func differentCheckSHA256(value string) string {
	if len(value) != 64 {
		return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	replacement := byte('a')
	if value[len(value)-1] == replacement {
		replacement = 'b'
	}
	return value[:len(value)-1] + string(replacement)
}
