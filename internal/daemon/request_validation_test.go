package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"reflect"
	"testing"

	"crewfold/internal/localapi"
)

func TestOperatorRequestParamContractRegistryIsComplete(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		localapi.MethodWorkspaceShow:               "local/v1/workspace-show.params.schema.json",
		localapi.MethodProjectShow:                 "local/v1/project-show.params.schema.json",
		localapi.MethodAgentShow:                   "local/v1/agent-query.params.schema.json",
		localapi.MethodWorkspaceList:               "local/v1/workspace-list.params.schema.json",
		localapi.MethodProjectList:                 "local/v1/project-list.params.schema.json",
		localapi.MethodAgentList:                   "local/v1/agent-list.params.schema.json",
		localapi.MethodDomainAgentCreate:           "local/v1/domain-agent-create.params.schema.json",
		localapi.MethodDomainAgentAttach:           "local/v1/domain-agent-attach.params.schema.json",
		localapi.MethodDomainAgentUpdate:           "local/v1/domain-agent-update.params.schema.json",
		localapi.MethodDomainAgentTree:             "local/v1/domain-agent-tree.params.schema.json",
		localapi.MethodDomainAgentSessionOpen:      "local/v1/domain-agent-session-open.params.schema.json",
		localapi.MethodDomainAgentSessionShow:      "local/v1/domain-agent-session.params.schema.json",
		localapi.MethodDomainAgentSessionSend:      "local/v1/domain-agent-session-send.params.schema.json",
		localapi.MethodDomainAgentSessionInterrupt: "local/v1/domain-agent-session-interrupt.params.schema.json",
		localapi.MethodDomainStaffingGrantCreate:   "local/v1/domain-staffing-grant-create.params.schema.json",
		localapi.MethodDomainStaffingGrantList:     "local/v1/domain-staffing-grant-list.params.schema.json",
		localapi.MethodDomainStaffingGrantRevoke:   "local/v1/domain-staffing-grant-revoke.params.schema.json",
		localapi.MethodObjectiveList:               "local/v1/objective-list.params.schema.json",
		localapi.MethodTaskList:                    "local/v1/task-list.params.schema.json",
		localapi.MethodRunList:                     "local/v1/run-list.params.schema.json",
		localapi.MethodClaimList:                   "local/v1/claim-list.params.schema.json",
		localapi.MethodOverlapList:                 "local/v1/overlap-list.params.schema.json",
		localapi.MethodDriftList:                   "local/v1/drift-list.params.schema.json",
		localapi.MethodMeetingList:                 "local/v1/meeting-list.params.schema.json",
		localapi.MethodApprovalList:                "local/v1/approval-list.params.schema.json",
		localapi.MethodCheckList:                   "local/v1/check-list.params.schema.json",
		localapi.MethodInboxList:                   "local/v1/inbox-list.params.schema.json",
		localapi.MethodEventsList:                  "local/v1/events-list.params.schema.json",
		localapi.MethodEventsTimeline:              "local/v1/events-timeline.params.schema.json",
		localapi.MethodBriefingShow:                "local/v1/briefing-show.params.schema.json",
		localapi.MethodBriefingExplain:             "local/v1/briefing-explain.params.schema.json",
		localapi.MethodSupervisorActionShow:        "local/v1/supervisor-action-query.params.schema.json",
		localapi.MethodRunAttach:                   "local/v1/run-attach.params.schema.json",
		localapi.MethodRunResume:                   "local/v1/run-resume.params.schema.json",
		localapi.MethodRunStop:                     "local/v1/run-stop.params.schema.json",
		localapi.MethodRunLostResolve:              "local/v1/run-lost-resolve.params.schema.json",
		localapi.MethodApprovalAllow:               "local/v1/approval-decision.params.schema.json",
		localapi.MethodApprovalDeny:                "local/v1/approval-decision.params.schema.json",
		localapi.MethodSystemDoctorFull:            "local/v1/system-doctor-full.params.schema.json",
		localapi.MethodBackupCreate:                "local/v1/backup-create.params.schema.json",
		localapi.MethodWebBootstrap:                "local/v1/web-bootstrap.params.schema.json",
		localapi.MethodOwnerCrewConfigure:          "local/v1/owner-crew-configure.params.schema.json",
	}
	if !reflect.DeepEqual(operatorRequestParamContracts, want) {
		t.Fatalf("operator request contract registry = %#v, want %#v", operatorRequestParamContracts, want)
	}
}

func TestLocalAPIIngressRejectsAmbiguousEnvelopeAndParams(t *testing.T) {
	t.Parallel()
	running := startTestServer(t, testConfig(t))
	for name, request := range map[string]string{
		"unknown envelope field":   `{"id":"unknown-envelope","protocol":1,"method":"system.status","unexpected":true}`,
		"duplicate envelope field": `{"id":"duplicate-envelope","protocol":1,"method":"system.status","method":"database.status"}`,
		"trailing request value":   `{"id":"trailing-envelope","protocol":1,"method":"system.status"} {}`,
		"unknown hello param":      `{"id":"hello-unknown","method":"system.hello","params":{"min_protocol":1,"max_protocol":1,"unexpected":true}}`,
		"duplicate hello param":    `{"id":"hello-duplicate","method":"system.hello","params":{"min_protocol":1,"min_protocol":1,"max_protocol":1}}`,
		"hello selected protocol":  `{"id":"hello-protocol","protocol":1,"method":"system.hello","params":{"min_protocol":1,"max_protocol":1}}`,
		"duplicate operator param": `{"id":"params-duplicate","protocol":1,"method":"agent.list","params":{"workspace":"personal","workspace":"other"}}`,
	} {
		name, request := name, request
		t.Run(name, func(t *testing.T) {
			response := sendRawLocalAPIRequest(t, running.config.SocketPath, request)
			assertInvalidLocalAPIRequest(t, response)
		})
	}
}

func TestOperatorRequestSchemasRejectPresenceSensitiveInvalidValues(t *testing.T) {
	t.Parallel()
	running := startTestServer(t, testConfig(t))
	for name, request := range map[string]string{
		"explicit zero page limit": `{"id":"zero-limit","protocol":1,"method":"agent.list","params":{"workspace":"personal","limit":0}}`,
		"malformed attach run":     `{"id":"attach-run","protocol":1,"method":"run.attach","params":{"workspace":"personal","run":"run_bad"}}`,
		"malformed resume run":     `{"id":"resume-run","protocol":1,"method":"run.resume","params":{"workspace":"personal","run":"run_bad","expected_revision":1,"idempotency_key":"resume"}}`,
		"malformed stop run":       `{"id":"stop-run","protocol":1,"method":"run.stop","params":{"workspace":"personal","run":"run_bad","expected_revision":1,"grace_period_millis":100,"idempotency_key":"stop"}}`,
		"full doctor has params":   `{"id":"doctor-extra","protocol":1,"method":"system.doctor.full","params":{"quick":true}}`,
		"relative backup target":   `{"id":"backup-relative","protocol":1,"method":"backup.create","params":{"target_path":"relative","idempotency_key":"backup"}}`,
		"empty backup key":         `{"id":"backup-key","protocol":1,"method":"backup.create","params":{"target_path":"/private/new-backup","idempotency_key":""}}`,
		"web bootstrap has params": `{"id":"web-extra","protocol":1,"method":"web.bootstrap","params":{"origin":"http://attacker.invalid"}}`,
	} {
		name, request := name, request
		t.Run(name, func(t *testing.T) {
			response := sendRawLocalAPIRequest(t, running.config.SocketPath, request)
			assertInvalidLocalAPIRequest(t, response)
		})
	}
}

func sendRawLocalAPIRequest(t *testing.T, socketPath, request string) localapi.Response {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial local API: %v", err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte(request + "\n")); err != nil {
		t.Fatalf("write raw local API request: %v", err)
	}
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read raw local API response: %v", err)
	}
	var response localapi.Response
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode raw local API response: %v", err)
	}
	return response
}

func assertInvalidLocalAPIRequest(t *testing.T, response localapi.Response) {
	t.Helper()
	if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable || len(response.Result) != 0 {
		t.Fatalf("response = %#v, want non-retryable invalid_request without a result", response)
	}
}
