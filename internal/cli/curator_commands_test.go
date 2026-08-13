package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestCuratorQueueForwardsBoundedCursorAndRendersEligibility(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{curatorQueue: localapi.CuratorQueueResult{
		Schema: localapi.CuratorQueueSchema,
		Type:   "curator_queue",
		Queue: domain.CuratorQueue{
			Rule: domain.CuratorRule{Name: domain.CuratorRuleAcceptedMeetingResolutionCopy, Revision: 1},
			Entries: []domain.CuratorQueueEntry{{
				Revision:          domain.KnowledgeRevision{ID: "krev_00000000000000000000000000000001", ReviewStatus: domain.KnowledgeReviewProposed, Title: "Accepted meeting resolution"},
				Eligibility:       domain.CuratorEligibilityManual,
				EligibilityReason: domain.CuratorEligibilityReasonRuleDisabled,
			}},
			NextCursor: "curator-cursor",
		},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"curator", "queue", "--workspace", "personal", "--project", "engine", "--after", "prior-cursor", "--limit", "37", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
		t.Fatalf("curator queue exit=%d stderr=%q", exit, stderr.String())
	}
	if got := client.curatorQueueParams; got.Workspace != "personal" || got.Project != "engine" || got.After != "prior-cursor" || got.Limit == nil || *got.Limit != 37 {
		t.Fatalf("CuratorQueue params = %#v", got)
	}
	for _, expected := range []string{"rule: accepted-meeting-resolution-copy", "rule enabled: false", "rule revision: 1", "entries: 1", "manual_review", "rule_disabled", "next cursor: curator-cursor"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("queue output %q lacks %q", stdout.String(), expected)
		}
	}
}

func TestCuratorQueueDefaultsToFiftyAndPreservesMachineSchema(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{curatorQueue: localapi.CuratorQueueResult{
		Schema: localapi.CuratorQueueSchema, Type: "curator_queue",
		Queue: domain.CuratorQueue{
			Rule:    domain.CuratorRule{Name: domain.CuratorRuleAcceptedMeetingResolutionCopy, Revision: 1},
			Entries: []domain.CuratorQueueEntry{},
		},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"curator", "queue", "--workspace", "personal", "--project", "engine", "--socket", "/tmp/crewfold.sock", "--output", "json"}); exit != ExitOK {
		t.Fatalf("curator queue JSON exit=%d stderr=%q", exit, stderr.String())
	}
	if client.curatorQueueParams.Limit == nil || *client.curatorQueueParams.Limit != 50 {
		t.Fatalf("default limit = %v, want 50", client.curatorQueueParams.Limit)
	}
	var decoded localapi.CuratorQueueResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded.Schema != localapi.CuratorQueueSchema ||
		decoded.Queue.Rule.Revision != 1 || decoded.Queue.Entries == nil {
		t.Fatalf("queue JSON = %#v, error=%v", decoded, err)
	}
}

func TestCuratorRuleMapsOnlyPublicAliasAndPreservesOwnerRevision(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		command string
		enabled bool
	}{
		{command: "enable", enabled: true},
		{command: "disable", enabled: false},
	} {
		t.Run(test.command, func(t *testing.T) {
			t.Parallel()
			client := &fakeDaemonClient{curatorRuleMutation: localapi.CuratorRuleMutationResult{
				Schema: localapi.CuratorRuleMutationSchema, Type: "curator_rule_mutation", EventSequence: 8,
				Rule: domain.CuratorRule{Name: domain.CuratorRuleAcceptedMeetingResolutionCopy, Revision: 1, Enabled: test.enabled},
			}}
			app, stdout, stderr := newTestApp()
			app.newClient = func(string) daemonClient { return client }
			if exit := app.Run([]string{"curator", "rule", test.command, curatorAcceptedMeetingResolutionRuleAlias, "--workspace", "personal", "--expected-revision", "1", "--idempotency-key", "rule-change", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
				t.Fatalf("curator rule %s exit=%d stderr=%q", test.command, exit, stderr.String())
			}
			got := client.curatorRuleParams
			if got.Rule != domain.CuratorRuleAcceptedMeetingResolutionCopy || got.Enabled == nil || *got.Enabled != test.enabled || got.ExpectedRevision != 1 || got.IdempotencyKey != "rule-change" {
				t.Fatalf("CuratorRuleConfigure params = %#v", got)
			}
			if !strings.Contains(stdout.String(), "rule: "+curatorAcceptedMeetingResolutionRuleAlias) {
				t.Fatalf("rule output = %q", stdout.String())
			}
		})
	}
}

func TestCuratorProcessDefaultsToDerivationOnlyAndAllowsExplicitSafeApplication(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{curatorProcess: localapi.CuratorProcessResult{
		Schema: localapi.CuratorProcessSchema, Type: "curator_process", EventSequence: 12,
		Process: domain.CuratorProcess{CandidatesScanned: 4, Derived: []domain.CuratorDerivation{}, Accepted: []domain.CuratorAutoAcceptance{}, Skipped: []domain.CuratorSkip{}},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"curator", "process", "--workspace", "personal", "--project", "engine", "--apply-safe", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
		t.Fatalf("curator process exit=%d stderr=%q", exit, stderr.String())
	}
	if got := client.curatorProcessParams; got.Workspace != "personal" || got.Project != "engine" || !got.ApplySafe {
		t.Fatalf("CuratorProcess params = %#v", got)
	}
	if !strings.Contains(stdout.String(), "candidates scanned: 4") || !strings.Contains(stdout.String(), "automatically accepted: 0") {
		t.Fatalf("process output = %q", stdout.String())
	}

	for _, test := range []struct {
		extra     []string
		applySafe bool
	}{
		{extra: nil, applySafe: false},
		{extra: []string{"--apply-safe=false"}, applySafe: false},
	} {
		app, _, stderr = newTestApp()
		app.newClient = func(string) daemonClient { return client }
		args := []string{"curator", "process", "--workspace", "personal", "--project", "engine", "--socket", "/tmp/crewfold.sock"}
		args = append(args, test.extra...)
		if exit := app.Run(args); exit != ExitOK || stderr.Len() != 0 {
			t.Fatalf("derive-only process args=%v exit=%d stderr=%q", test.extra, exit, stderr.String())
		}
		if client.curatorProcessParams.ApplySafe != test.applySafe {
			t.Fatalf("derive-only process args=%v apply_safe=%t", test.extra, client.curatorProcessParams.ApplySafe)
		}
	}
}

func TestCuratorCommandsRejectUnboundedOrUnknownInputsAndPublishHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"curator", "queue", "--workspace", "personal", "--project", "engine", "--limit", "0", "--socket", "/tmp/crewfold.sock"},
		{"curator", "queue", "--workspace", "personal", "--project", "engine", "--limit", "201", "--socket", "/tmp/crewfold.sock"},
		{"curator", "rule", "enable", "anything-else", "--workspace", "personal", "--expected-revision", "1", "--socket", "/tmp/crewfold.sock"},
	} {
		app, _, stderr := newTestApp()
		if exit := app.Run(args); exit != ExitUsage || stderr.Len() == 0 {
			t.Fatalf("args=%v exit=%d stderr=%q", args, exit, stderr.String())
		}
	}
	app, stdout, stderr := newTestApp()
	if exit := app.Run([]string{"help", "curator"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("curator help exit=%d stderr=%q", exit, stderr.String())
	}
	for _, expected := range []string{"curator queue", "curator rule enable accepted-meeting-resolution-copy", "curator rule disable accepted-meeting-resolution-copy", "curator process", "--apply-safe"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("curator help %q lacks %q", stdout.String(), expected)
		}
	}
}
