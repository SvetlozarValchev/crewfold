package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestCheckNotificationsUseOnlyCurrentExactOwnersAndRoutes(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	sameRole := fixture.agent.Role
	grantedRouteAgent := createCheckNotificationAgent(t, fixture, "exact-route-agent", sameRole)
	ungrantedSameRole := createCheckNotificationAgent(t, fixture, "ungranted-same-role-agent", sameRole)
	route, err := fixture.storage.CreateCheckRoute(context.Background(), CreateCheckRouteCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		CheckDefinitionID: fixture.definition.ID, DefinitionContentRevision: fixture.definition.ContentRevision,
		Trigger: domain.CheckRouteNonpass, Duty: domain.CheckDutyEvidenceReview,
		AgentIdentifier: grantedRouteAgent.ID, ExpectedAgentRevision: grantedRouteAgent.Revision,
		IdempotencyKey: "exact-check-notification-route", CorrelationID: "exact-check-notification-route",
	})
	if err != nil {
		t.Fatalf("CreateCheckRoute() error = %v", err)
	}
	finished := finishExistingCheckFixture(t, fixture, domain.CheckOutcomeFailed)
	if len(finished.Notifications) != 2 || len(finished.RouteFailures) != 0 {
		t.Fatalf("finished notification bundle = notices %#v, failures %#v; want exact owner+route", finished.Notifications, finished.RouteFailures)
	}
	want := map[string]struct {
		duty, routeID, assignmentID string
		revision                    int64
	}{
		fixture.agent.ID:     {duty: domain.CheckDutyTaskOwner, assignmentID: fixture.task.Task.AssignmentID, revision: fixture.agent.Revision},
		grantedRouteAgent.ID: {duty: domain.CheckDutyEvidenceReview, routeID: route.Value.ID, revision: grantedRouteAgent.Revision},
	}
	for _, notice := range finished.Notifications {
		expected, ok := want[notice.RecipientAgentID]
		if !ok || notice.Duty != expected.duty || notice.RouteID != expected.routeID || notice.AssignmentID != expected.assignmentID || notice.RecipientAgentRevision != expected.revision {
			t.Fatalf("notification receipt = %#v, expected exact entry %#v", notice, expected)
		}
		inbox, err := fixture.storage.Inbox(context.Background(), fixture.workspace.ID, notice.RecipientAgentID, 10)
		if err != nil || len(inbox) != 1 {
			t.Fatalf("Inbox(%s) = %#v, %v", notice.RecipientAgentID, inbox, err)
		}
		message := inbox[0].Message
		if message.ID != notice.MessageID || message.SenderType != "subsystem" || message.SenderID != "crewfold-check-worker" || message.SenderAgentID != "" || message.SenderRunID != "" {
			t.Fatalf("subsystem inbox message = %#v, receipt %#v", message, notice)
		}
	}
	if inbox, err := fixture.storage.Inbox(context.Background(), fixture.workspace.ID, ungrantedSameRole.ID, 10); err != nil || len(inbox) != 0 {
		t.Fatalf("ungranted same-role Inbox() = %#v, %v; want empty", inbox, err)
	}
}

func TestCheckNonpassRoutesToExpiredButReservedCurrentOwner(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	fixture.now = fixture.now.Add(10 * time.Minute)
	finished := finishExistingCheckFixture(t, fixture, domain.CheckOutcomeFailed)
	if len(finished.Notifications) != 1 || len(finished.RouteFailures) != 0 {
		t.Fatalf("expired-but-reserved owner routing = notices %#v failures %#v; want exact current owner", finished.Notifications, finished.RouteFailures)
	}
	notice := finished.Notifications[0]
	if notice.Duty != domain.CheckDutyTaskOwner || notice.RecipientAgentID != fixture.agent.ID || notice.AssignmentID != fixture.task.Task.AssignmentID || notice.AssignmentRevision != fixture.task.Assignment.Revision {
		t.Fatalf("reserved task-owner receipt = %#v", notice)
	}
	if inbox, err := fixture.storage.Inbox(context.Background(), fixture.workspace.ID, fixture.agent.ID, 10); err != nil || len(inbox) != 1 || inbox[0].Message.ID != notice.MessageID {
		t.Fatalf("reserved task-owner Inbox() = %#v, %v", inbox, err)
	}
}

func TestCheckNonpassWithoutReservedCurrentOwnerRecordsUnroutableOnly(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	requested, err := fixture.storage.RunGrantedCheck(context.Background(), RequestGrantedCheckRunCommand{
		SourceRunID: fixture.sourceRun.ID, CheckWatchGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		RequirementID: fixture.requirement.ID, IdempotencyKey: "request-unreserved-owner-check", CorrelationID: "request-unreserved-owner-check",
	})
	if err != nil {
		t.Fatalf("RunGrantedCheck() error = %v", err)
	}
	work, found, err := fixture.storage.ClaimCheckJob(context.Background(), 20*time.Minute)
	if err != nil || !found || work.Run.ID != requested.Value.ID {
		t.Fatalf("ClaimCheckJob() = found %t, work %#v, %v", found, work, err)
	}
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "unreserved-owner-runtime-binding"); err != nil {
		t.Fatal(err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "unreserved-owner-running"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.storage.FailRunStart(context.Background(), fixture.sourceRun.ID, "watcher runtime never launched", "unreserve-check-owner-assignment"); err != nil {
		t.Fatalf("FailRunStart(source watcher) error = %v", err)
	}
	fixture.now = fixture.now.Add(10 * time.Minute)
	exitCode := 1
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID: work.Run.ID, Outcome: domain.CheckOutcomeFailed, ExitCode: &exitCode,
		TerminalObservation: terminal, CorrelationID: "finish-unreserved-owner-check",
	})
	if err != nil {
		t.Fatalf("FinishCheckRun() error = %v", err)
	}
	if len(finished.Notifications) != 0 || len(finished.RouteFailures) != 1 {
		t.Fatalf("expired unreserved-owner routing = notices %#v failures %#v", finished.Notifications, finished.RouteFailures)
	}
	failure := finished.RouteFailures[0]
	if failure.Code != "unroutable" || failure.Duty != domain.CheckDutyTaskOwner || failure.RecipientAgentID != "" || failure.AssignmentID != "" || failure.RouteID != "" {
		t.Fatalf("unroutable receipt guessed historical authority: %#v", failure)
	}
	if inbox, err := fixture.storage.Inbox(context.Background(), fixture.workspace.ID, fixture.agent.ID, 10); err != nil || len(inbox) != 0 {
		t.Fatalf("expired unreserved historical owner Inbox() = %#v, %v; want empty", inbox, err)
	}
}

func TestRawSQLCannotForgeOrDetachCheckSubsystemMessage(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	other := createCheckNotificationAgent(t, fixture, "raw-notification-other-agent", fixture.agent.Role)
	finished := finishExistingCheckFixture(t, fixture, domain.CheckOutcomeFailed)
	if len(finished.Notifications) != 1 {
		t.Fatalf("notifications = %#v, want task owner", finished.Notifications)
	}
	notice := finished.Notifications[0]
	now := fixture.now.Add(time.Second).Format(time.RFC3339Nano)
	threadID := "thread_" + strings.Repeat("d", 32)
	if _, err := fixture.storage.db.Exec(`INSERT INTO message_threads(id,workspace_id,project_id,task_id,subject,status,revision,created_at,updated_at,created_by,updated_by,kind,participant_revision,initial_participant_count)
		VALUES(?,?,?,?,?,'open',1,?,?,? ,?,'direct',0,0)`, threadID, fixture.workspace.ID, fixture.project.ID, fixture.task.Task.ID, "forged subsystem thread", now, now, "local-owner", "local-owner"); err != nil {
		t.Fatalf("seed direct thread: %v", err)
	}
	if _, err := fixture.storage.db.Exec(`INSERT INTO messages(id,workspace_id,thread_id,project_id,task_id,sender_type,sender_id,kind,body,artifact_ids_json,created_at)
		VALUES(?,?,?,?,?,'subsystem','crewfold-check-worker','inform','forged raw subsystem message','[]',?)`, "msg_"+strings.Repeat("e", 32), fixture.workspace.ID, threadID, fixture.project.ID, fixture.task.Task.ID, now); err == nil {
		t.Fatal("raw SQL forged a detached check subsystem message")
	}
	for _, attack := range []struct {
		name, statement string
		arguments       []any
	}{
		{"detach receipt", `UPDATE check_notification_receipts SET message_id=? WHERE id=?`, []any{"msg_" + strings.Repeat("e", 32), notice.ID}},
		{"delete receipt", `DELETE FROM check_notification_receipts WHERE id=?`, []any{notice.ID}},
		{"change subsystem sender", `UPDATE messages SET sender_id='local-owner' WHERE id=?`, []any{notice.MessageID}},
		{"delete subsystem message", `DELETE FROM messages WHERE id=?`, []any{notice.MessageID}},
		{"retarget subsystem recipient", `UPDATE message_recipients SET recipient_agent_id=? WHERE message_id=?`, []any{other.ID, notice.MessageID}},
	} {
		if _, err := fixture.storage.db.Exec(attack.statement, attack.arguments...); err == nil {
			t.Fatalf("raw SQL attack %q succeeded", attack.name)
		}
	}
	read, err := fixture.storage.CheckRunDetail(context.Background(), fixture.workspace.ID, finished.Run.ID)
	if err != nil || len(read.Notifications) != 1 || read.Notifications[0].MessageID != notice.MessageID {
		t.Fatalf("CheckRunDetail(after attacks) = %#v, %v", read, err)
	}
}

func createCheckNotificationAgent(t *testing.T, fixture *grantedCheckAuthorityFixture, name, role string) domain.AgentDefinition {
	t.Helper()
	created, err := fixture.storage.CreateAgent(context.Background(), CreateAgentCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Name: name, Role: role, Provider: "fake", Runtime: "fake",
		IdempotencyKey: "create-" + name, CorrelationID: "create-" + name,
	})
	if err != nil {
		t.Fatalf("CreateAgent(%s) error = %v", name, err)
	}
	return created.Value
}

func finishExistingCheckFixture(t *testing.T, fixture *grantedCheckAuthorityFixture, outcome string) domain.CheckRunDetail {
	t.Helper()
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "notification-runtime-binding"); err != nil {
		t.Fatal(err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "notification-running"); err != nil {
		t.Fatal(err)
	}
	fixture.advance()
	exitCode := 0
	if outcome == domain.CheckOutcomeFailed {
		exitCode = 1
	}
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID: work.Run.ID, Outcome: outcome, ExitCode: &exitCode, TerminalObservation: terminal, CorrelationID: "notification-finish-" + outcome,
	})
	if err != nil {
		t.Fatalf("FinishCheckRun(%s) error = %v", outcome, err)
	}
	return finished
}
