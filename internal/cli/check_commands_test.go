package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestCheckDefinitionCreatePreservesOrderedFixedArguments(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := newTestApp()
	client := newFakeCheckClient()
	client.definitionMutation = localapi.CheckDefinitionMutationResult{Schema: localapi.CheckDefinitionMutationSchema, Type: "check_definition_mutation", Definition: domain.CheckDefinition{ID: "checkdef_exact", Name: "unit", Status: domain.CheckDefinitionActive, ContentRevision: 1, Revision: 1}}
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"check", "definition", "create", "unit", "--workspace", "personal", "--project", "demo", "--executable", "/usr/local/bin/go", "--arg", "test", "--arg=", "--arg", "./...", "--working-directory", ".", "--timeout", "10m", "--output-byte-limit", "65536", "--idempotency-key", "definition-create", "--socket", "/tmp/crewfold.sock", "--output", "json"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run() exit = %d, stderr = %q", exit, stderr.String())
	}
	if got := strings.Join(client.definitionCreate.Arguments, "|"); got != "test||./..." || client.definitionCreate.TimeoutMillis != 600000 || client.definitionCreate.OutputByteLimit != 65536 || client.definitionCreate.IdempotencyKey == "" {
		t.Fatalf("definition create params = %#v", client.definitionCreate)
	}
	if !strings.Contains(stdout.String(), localapi.CheckDefinitionMutationSchema) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestCheckDefinitionCreatePreservesEmptyArgumentVectorAsArray(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp()
	client := newFakeCheckClient()
	client.definitionMutation = localapi.CheckDefinitionMutationResult{Schema: localapi.CheckDefinitionMutationSchema, Type: "check_definition_mutation", Definition: domain.CheckDefinition{ID: "checkdef_exact", Name: "unit", Status: domain.CheckDefinitionActive, ContentRevision: 1, Revision: 1}}
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"check", "definition", "create", "unit", "--workspace", "personal", "--project", "demo", "--executable", "/usr/local/bin/go", "--working-directory", ".", "--timeout", "10m", "--output-byte-limit", "65536", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run() exit = %d, stderr = %q", exit, stderr.String())
	}
	if client.definitionCreate.Arguments == nil || len(client.definitionCreate.Arguments) != 0 {
		t.Fatalf("definition arguments = %#v, want non-nil empty vector", client.definitionCreate.Arguments)
	}
}

func TestCheckGrantCreateFreezesDefinitionsAndClosedOperations(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp()
	client := newFakeCheckClient()
	client.grantMutation = localapi.CheckGrantMutationResult{Schema: localapi.CheckGrantMutationSchema, Type: "check_watch_grant_mutation", Grant: domain.CheckWatchGrant{ID: "checkgrant_exact", Status: domain.CheckWatchGrantActive, Revision: 1}}
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"check", "grant", "create", "--workspace", "personal", "--project", "demo", "--agent", "watcher", "--expected-agent-revision", "4", "--definition", "unit@2", "--definition", "lint@7", "--operation", "inspect", "--operation", "run", "--max-pending", "8", "--max-in-flight", "2", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run() exit = %d, stderr = %q", exit, stderr.String())
	}
	if len(client.grantCreate.Definitions) != 2 || client.grantCreate.Definitions[0].Definition != "unit" || client.grantCreate.Definitions[0].ContentRevision != 2 || client.grantCreate.Definitions[1].Definition != "lint" || client.grantCreate.Definitions[1].ContentRevision != 7 {
		t.Fatalf("grant definitions = %#v", client.grantCreate.Definitions)
	}
	if got := strings.Join(client.grantCreate.Operations, ","); got != "inspect,run" || client.grantCreate.ExpectedAgentRevision != 4 {
		t.Fatalf("grant params = %#v", client.grantCreate)
	}
}

func TestCheckRunVisibleFormAllowsAtomicRevisionResolution(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp()
	client := newFakeCheckClient()
	client.checkRunMutation = localapi.CheckRunMutationResult{Schema: localapi.CheckRunMutationSchema, Type: "check_run_mutation", Run: domain.CheckRun{ID: "checkrun_exact", Status: domain.CheckRunRequested}}
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"check", "run", "unit", "--task", "task_exact", "--workspace", "personal", "--idempotency-key", "check-run", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run() exit = %d, stderr = %q", exit, stderr.String())
	}
	if client.checkRun.Definition != "unit" || client.checkRun.Task != "task_exact" || client.checkRun.ExpectedRequirementRevision != 0 || client.checkRun.ExpectedDefinitionContentRevision != 0 || client.checkRun.ExpectedCheckoutRevision != 0 || client.checkRun.IdempotencyKey == "" {
		t.Fatalf("check run params = %#v", client.checkRun)
	}
}

func TestCheckRunRejectsCheckoutRevisionWithoutCheckout(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp()
	exit := app.Run([]string{"check", "run", "unit", "--task", "task_exact", "--workspace", "personal", "--expected-checkout-revision", "3", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitUsage || !strings.Contains(stderr.String(), "requires --checkout") {
		t.Fatalf("Run() exit = %d, stderr = %q", exit, stderr.String())
	}
}

func TestCheckPolicyConfigureRejectsOutOfRangeOpenRepairLimit(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp()
	exit := app.Run([]string{"check", "policy", "configure", "--workspace", "personal", "--project", "demo", "--repair-proposals", "disabled", "--max-open-repairs", "0", "--expected-revision", "1", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitUsage || !strings.Contains(stderr.String(), "from 1 to 32") {
		t.Fatalf("Run() exit = %d, stderr = %q", exit, stderr.String())
	}
}

func TestRunStartCheckWatchGrantPairAndContextExclusion(t *testing.T) {
	t.Parallel()
	scenarioPath := filepath.Join(t.TempDir(), "scenario.json")
	scenario := `{"schema":"urn:crewfold:schema:fixture:fake-run-scenario:v1","name":"watcher","steps":[{"kind":"completion","message":"done","handoff":"owner review"}]}`
	if err := os.WriteFile(scenarioPath, []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"run", "start", "task_exact", "--workspace", "personal", "--runtime", "fake", "--provider", "fake", "--scenario", scenarioPath, "--expected-task-revision", "2", "--socket", "/tmp/crewfold.sock"}
	for _, test := range []struct {
		name  string
		extra []string
	}{
		{name: "grant without revision", extra: []string{"--check-watch-grant", "checkgrant_exact"}},
		{name: "revision without grant", extra: []string{"--expected-check-watch-grant-revision", "3"}},
		{name: "context with grant", extra: []string{"--context", "ctx_exact", "--check-watch-grant", "checkgrant_exact", "--expected-check-watch-grant-revision", "3"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			exit := app.Run(append(append([]string{}, base...), test.extra...))
			if exit != ExitUsage {
				t.Fatalf("Run() exit = %d, stderr = %q", exit, stderr.String())
			}
		})
	}
	app, _, stderr := newTestApp()
	client := &fakeDaemonClient{runMutation: localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation"}}
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run(append(base, "--check-watch-grant", "checkgrant_exact", "--expected-check-watch-grant-revision", "3"))
	if exit != ExitOK || stderr.Len() != 0 || client.runStartParams.CheckWatchGrant != "checkgrant_exact" || client.runStartParams.ExpectedCheckWatchGrantRevision != 3 || client.runStartParams.Context != "" {
		t.Fatalf("Run() exit = %d, stderr = %q, params = %#v", exit, stderr.String(), client.runStartParams)
	}
}

type fakeCheckDaemonClient struct {
	*fakeDaemonClient
	definitionCreate   localapi.CheckDefinitionCreateParams
	definitionMutation localapi.CheckDefinitionMutationResult
	grantCreate        localapi.CheckGrantCreateParams
	grantMutation      localapi.CheckGrantMutationResult
	checkRun           localapi.CheckRunParams
	checkRunMutation   localapi.CheckRunMutationResult
}

func newFakeCheckClient() *fakeCheckDaemonClient {
	return &fakeCheckDaemonClient{fakeDaemonClient: &fakeDaemonClient{}}
}

func (c *fakeCheckDaemonClient) CheckDefinitionCreate(_ context.Context, p localapi.CheckDefinitionCreateParams) (localapi.CheckDefinitionMutationResult, error) {
	c.definitionCreate = p
	return c.definitionMutation, nil
}
func (c *fakeCheckDaemonClient) CheckDefinitionRetire(context.Context, localapi.CheckDefinitionRetireParams) (localapi.CheckDefinitionMutationResult, error) {
	return c.definitionMutation, nil
}
func (c *fakeCheckDaemonClient) CheckDefinitionShow(context.Context, string, string) (localapi.CheckDefinitionShowResult, error) {
	return localapi.CheckDefinitionShowResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckDefinitionList(context.Context, localapi.CheckDefinitionQueryParams) (localapi.CheckDefinitionListResult, error) {
	return localapi.CheckDefinitionListResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRequirementCreate(context.Context, localapi.CheckRequirementCreateParams) (localapi.CheckRequirementMutationResult, error) {
	return localapi.CheckRequirementMutationResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRequirementRetire(context.Context, localapi.CheckRequirementRetireParams) (localapi.CheckRequirementMutationResult, error) {
	return localapi.CheckRequirementMutationResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRequirementList(context.Context, localapi.CheckRequirementQueryParams) (localapi.CheckRequirementListResult, error) {
	return localapi.CheckRequirementListResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckGrantCreate(_ context.Context, p localapi.CheckGrantCreateParams) (localapi.CheckGrantMutationResult, error) {
	c.grantCreate = p
	return c.grantMutation, nil
}
func (c *fakeCheckDaemonClient) CheckGrantRevoke(context.Context, localapi.CheckGrantRevokeParams) (localapi.CheckGrantMutationResult, error) {
	return c.grantMutation, nil
}
func (c *fakeCheckDaemonClient) CheckGrantShow(context.Context, string, string) (localapi.CheckGrantShowResult, error) {
	return localapi.CheckGrantShowResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckGrantList(context.Context, localapi.CheckGrantQueryParams) (localapi.CheckGrantListResult, error) {
	return localapi.CheckGrantListResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRouteCreate(context.Context, localapi.CheckRouteCreateParams) (localapi.CheckRouteMutationResult, error) {
	return localapi.CheckRouteMutationResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRouteRetire(context.Context, localapi.CheckRouteRetireParams) (localapi.CheckRouteMutationResult, error) {
	return localapi.CheckRouteMutationResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRouteList(context.Context, localapi.CheckRouteQueryParams) (localapi.CheckRouteListResult, error) {
	return localapi.CheckRouteListResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckPolicyShow(context.Context, string, string) (localapi.CheckPolicyShowResult, error) {
	return localapi.CheckPolicyShowResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckPolicyConfigure(context.Context, localapi.CheckPolicyConfigureParams) (localapi.CheckPolicyMutationResult, error) {
	return localapi.CheckPolicyMutationResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRun(_ context.Context, p localapi.CheckRunParams) (localapi.CheckRunMutationResult, error) {
	c.checkRun = p
	return c.checkRunMutation, nil
}
func (c *fakeCheckDaemonClient) CheckList(context.Context, localapi.CheckQueryParams) (localapi.CheckRunListResult, error) {
	return localapi.CheckRunListResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckInspect(context.Context, string, string) (localapi.CheckInspectResult, error) {
	return localapi.CheckInspectResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckLogs(context.Context, string, string) (localapi.CheckLogsResult, error) {
	return localapi.CheckLogsResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckWatch(context.Context, localapi.CheckWatchParams) (localapi.CheckWatchResult, error) {
	return localapi.CheckWatchResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRepairList(context.Context, localapi.CheckRepairQueryParams) (localapi.CheckRepairListResult, error) {
	return localapi.CheckRepairListResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRepairInspect(context.Context, string, string) (localapi.CheckRepairShowResult, error) {
	return localapi.CheckRepairShowResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRepairAccept(context.Context, localapi.CheckRepairDecisionParams) (localapi.CheckRepairMutationResult, error) {
	return localapi.CheckRepairMutationResult{}, nil
}
func (c *fakeCheckDaemonClient) CheckRepairReject(context.Context, localapi.CheckRepairDecisionParams) (localapi.CheckRepairMutationResult, error) {
	return localapi.CheckRepairMutationResult{}, nil
}
