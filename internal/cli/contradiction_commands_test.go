package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestContradictionReportForwardsOnlyExactRevisionsReasonAndOwnerScope(t *testing.T) {
	t.Parallel()
	detail := contradictionCLITestDetail()
	client := &fakeDaemonClient{conMutation: localapi.ContradictionMutationResult{
		Schema: localapi.ContradictionMutationSchema, Type: "contradiction_mutation", Detail: detail, EventSequence: 12,
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{
		"contradiction", "report", detail.RightRevision.ID, detail.LeftRevision.ID,
		"--workspace", "personal", "--reason", "The exact values conflict.",
		"--idempotency-key", "report-conflict", "--socket", "/tmp/crewfold.sock",
	})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("report exit=%d stderr=%q", exit, stderr.String())
	}
	params := client.conReportParams
	if params.Workspace != "personal" || params.LeftRevision != detail.RightRevision.ID ||
		params.RightRevision != detail.LeftRevision.ID || params.Reason != "The exact values conflict." ||
		params.IdempotencyKey != "report-conflict" {
		t.Fatalf("ContradictionReport params = %#v", params)
	}
	for _, wanted := range []string{detail.Contradiction.ID, "proposed", "project-wide", "task=" + detail.RightRevision.TaskScopeID, "event sequence: 12"} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Errorf("report output %q lacks %q", stdout.String(), wanted)
		}
	}
}

func TestContradictionShowExplainsParticipantsApplicabilityAndAuthority(t *testing.T) {
	t.Parallel()
	detail := contradictionCLITestDetail()
	detail.Contradiction.Status = domain.KnowledgeContradictionOpen
	detail.Contradiction.StateRevision = 2
	detail.AuthorityCheckCount = 1
	detail.AuthorityChecks = []domain.KnowledgeContradictionAuthorityCheck{{
		ID: "kcauth_00000000000000000000000000000001", Action: domain.KnowledgeContradictionAuthorityConfirm,
		Outcome: domain.KnowledgeAuthorityAllowed, Reason: domain.KnowledgeAuthorityReasonOwner,
	}}
	client := &fakeDaemonClient{conShow: localapi.ContradictionShowResult{
		Schema: localapi.ContradictionShowSchema, Type: "knowledge_contradiction_detail", Detail: detail,
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"contradiction", "show", detail.Contradiction.ID, "--workspace", "personal", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("show exit=%d stderr=%q", exit, stderr.String())
	}
	for _, wanted := range []string{"open", "revision=2", detail.LeftRevision.ID, "project-wide", detail.RightRevision.ID, "task=" + detail.RightRevision.TaskScopeID, "authority checks: 1 displayed / 1 total", "confirm", "workspace_owner"} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Errorf("show output %q lacks %q", stdout.String(), wanted)
		}
	}
}

func TestContradictionListIsBoundedFilteredAndPreservesMachineContract(t *testing.T) {
	t.Parallel()
	detail := contradictionCLITestDetail()
	client := &fakeDaemonClient{conList: localapi.ContradictionListResult{
		Schema: localapi.ContradictionListSchema, Type: "knowledge_contradiction_list",
		List: domain.KnowledgeContradictionList{Details: []domain.KnowledgeContradictionDetail{detail}},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{
		"contradiction", "list", "--workspace", "personal", "--project", "engine", "--status", "proposed",
		"--revision", detail.LeftRevision.ID, "--limit", "37", "--socket", "/tmp/crewfold.sock", "--output", "json",
	}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("list exit=%d stderr=%q", exit, stderr.String())
	}
	params := client.conListParams
	if params.Workspace != "personal" || params.Project != "engine" || params.Status != "proposed" ||
		params.Revision != detail.LeftRevision.ID || params.Limit == nil || *params.Limit != 37 {
		t.Fatalf("ContradictionList params = %#v", params)
	}
	var decoded localapi.ContradictionListResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded.Schema != localapi.ContradictionListSchema || len(decoded.List.Details) != 1 {
		t.Fatalf("list JSON = %#v, error=%v", decoded, err)
	}
}

func TestContradictionConfirmAndDismissPreserveOptimisticRevision(t *testing.T) {
	t.Parallel()
	detail := contradictionCLITestDetail()
	for _, action := range []string{"confirm", "dismiss"} {
		t.Run(action, func(t *testing.T) {
			client := &fakeDaemonClient{conMutation: localapi.ContradictionMutationResult{
				Schema: localapi.ContradictionMutationSchema, Type: "contradiction_mutation", Detail: detail, EventSequence: 14,
			}}
			app, _, stderr := newTestApp()
			app.newClient = func(string) daemonClient { return client }
			exit := app.Run([]string{
				"contradiction", action, detail.Contradiction.ID, "--workspace", "personal",
				"--expected-state-revision", "3", "--note", "Owner review", "--idempotency-key", action + "-key",
				"--socket", "/tmp/crewfold.sock",
			})
			if exit != ExitOK || stderr.Len() != 0 {
				t.Fatalf("%s exit=%d stderr=%q", action, exit, stderr.String())
			}
			params := client.conDecisionParams
			if client.conAction != action || params.Workspace != "personal" ||
				params.Contradiction != detail.Contradiction.ID || params.ExpectedStateRevision != 3 ||
				params.Note != "Owner review" || params.IdempotencyKey != action+"-key" {
				t.Fatalf("%s params=%#v action=%q", action, params, client.conAction)
			}
		})
	}
}

func TestContradictionCommandsRejectUnboundedUnknownAndIncompleteInputs(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"contradiction", "report", "left-only", "--workspace", "personal", "--reason", "conflict", "--socket", "/tmp/crewfold.sock"},
		{"contradiction", "report", "left", "right", "--workspace", "personal", "--socket", "/tmp/crewfold.sock"},
		{"contradiction", "list", "--workspace", "personal", "--limit", "0", "--socket", "/tmp/crewfold.sock"},
		{"contradiction", "list", "--workspace", "personal", "--socket", "/tmp/crewfold.sock"},
		{"contradiction", "list", "--workspace", "personal", "--limit", "201", "--socket", "/tmp/crewfold.sock"},
		{"contradiction", "confirm", "kcon", "--workspace", "personal", "--expected-state-revision", "0", "--socket", "/tmp/crewfold.sock"},
		{"contradiction", "anything"},
	}
	for _, args := range tests {
		app, _, stderr := newTestApp()
		if exit := app.Run(args); exit != ExitUsage || stderr.Len() == 0 {
			t.Errorf("args=%v exit=%d stderr=%q", args, exit, stderr.String())
		}
	}
	app, stdout, stderr := newTestApp()
	if exit := app.Run([]string{"help", "contradiction"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("help exit=%d stderr=%q", exit, stderr.String())
	}
	for _, wanted := range []string{"contradiction report", "contradiction show", "contradiction list", "contradiction confirm", "contradiction dismiss", "effectively disputed"} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Errorf("help %q lacks %q", stdout.String(), wanted)
		}
	}
}

func contradictionCLITestDetail() domain.KnowledgeContradictionDetail {
	leftID := "krev_00000000000000000000000000000001"
	rightID := "krev_00000000000000000000000000000002"
	return domain.KnowledgeContradictionDetail{
		Contradiction: domain.KnowledgeContradiction{
			ID: "kcon_00000000000000000000000000000001", ProjectID: "prj_00000000000000000000000000000001",
			LeftRevisionID: leftID, RightRevisionID: rightID, Status: domain.KnowledgeContradictionProposed,
			StateRevision: 1, ReportNote: "The exact values conflict.", ReportedBy: "local-owner", ReportedByType: domain.KnowledgeActorHuman,
		},
		LeftRevision:    domain.KnowledgeRevision{ID: leftID, Title: "Project-wide contract"},
		RightRevision:   domain.KnowledgeRevision{ID: rightID, TaskScopeID: "task_00000000000000000000000000000001", Title: "Task exception"},
		AuthorityChecks: []domain.KnowledgeContradictionAuthorityCheck{},
	}
}
