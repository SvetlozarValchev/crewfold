package localapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckClientsCoverStrictResultDiscriminators(t *testing.T) {
	t.Parallel()
	type result struct {
		Schema string `json:"schema"`
		Type   string `json:"type"`
	}
	if len(checkResultDiscriminators) != 25 {
		t.Fatalf("check discriminator method count = %d, want 25", len(checkResultDiscriminators))
	}
	for method, expected := range checkResultDiscriminators {
		method, expected := method, expected
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			if err := validateCheckResultDiscriminator(method, &result{Schema: expected[0], Type: expected[1]}); err != nil {
				t.Fatalf("valid discriminator rejected: %v", err)
			}
			for name, value := range map[string]result{
				"schema": {Schema: "urn:wrong", Type: expected[1]},
				"type":   {Schema: expected[0], Type: "wrong"},
			} {
				if err := validateCheckResultDiscriminator(method, &value); err == nil || !strings.Contains(err.Error(), "discriminator") {
					t.Errorf("%s mismatch error = %v, want discriminator failure", name, err)
				}
			}
		})
	}
}

func TestEveryCheckClientRejectsWrongResultDiscriminator(t *testing.T) {
	t.Parallel()
	calls := map[string]func(*Client) error{
		MethodCheckDefinitionCreate: func(c *Client) error {
			_, err := c.CheckDefinitionCreate(context.Background(), CheckDefinitionCreateParams{})
			return err
		},
		MethodCheckDefinitionRetire: func(c *Client) error {
			_, err := c.CheckDefinitionRetire(context.Background(), CheckDefinitionRetireParams{})
			return err
		},
		MethodCheckDefinitionShow: func(c *Client) error {
			_, err := c.CheckDefinitionShow(context.Background(), "personal", "unit")
			return err
		},
		MethodCheckDefinitionList: func(c *Client) error {
			_, err := c.CheckDefinitionList(context.Background(), CheckDefinitionQueryParams{})
			return err
		},
		MethodCheckRequirementCreate: func(c *Client) error {
			_, err := c.CheckRequirementCreate(context.Background(), CheckRequirementCreateParams{})
			return err
		},
		MethodCheckRequirementRetire: func(c *Client) error {
			_, err := c.CheckRequirementRetire(context.Background(), CheckRequirementRetireParams{})
			return err
		},
		MethodCheckRequirementList: func(c *Client) error {
			_, err := c.CheckRequirementList(context.Background(), CheckRequirementQueryParams{})
			return err
		},
		MethodCheckGrantCreate: func(c *Client) error {
			_, err := c.CheckGrantCreate(context.Background(), CheckGrantCreateParams{})
			return err
		},
		MethodCheckGrantRevoke: func(c *Client) error {
			_, err := c.CheckGrantRevoke(context.Background(), CheckGrantRevokeParams{})
			return err
		},
		MethodCheckGrantShow: func(c *Client) error {
			_, err := c.CheckGrantShow(context.Background(), "personal", "grant")
			return err
		},
		MethodCheckGrantList: func(c *Client) error {
			_, err := c.CheckGrantList(context.Background(), CheckGrantQueryParams{})
			return err
		},
		MethodCheckRouteCreate: func(c *Client) error {
			_, err := c.CheckRouteCreate(context.Background(), CheckRouteCreateParams{})
			return err
		},
		MethodCheckRouteRetire: func(c *Client) error {
			_, err := c.CheckRouteRetire(context.Background(), CheckRouteRetireParams{})
			return err
		},
		MethodCheckRouteList: func(c *Client) error {
			_, err := c.CheckRouteList(context.Background(), CheckRouteQueryParams{})
			return err
		},
		MethodCheckPolicyShow: func(c *Client) error {
			_, err := c.CheckPolicyShow(context.Background(), "personal", "demo")
			return err
		},
		MethodCheckPolicyConfigure: func(c *Client) error {
			_, err := c.CheckPolicyConfigure(context.Background(), CheckPolicyConfigureParams{})
			return err
		},
		MethodCheckRun:  func(c *Client) error { _, err := c.CheckRun(context.Background(), CheckRunParams{}); return err },
		MethodCheckList: func(c *Client) error { _, err := c.CheckList(context.Background(), CheckQueryParams{}); return err },
		MethodCheckInspect: func(c *Client) error {
			_, err := c.CheckInspect(context.Background(), "personal", "checkrun")
			return err
		},
		MethodCheckLogs:  func(c *Client) error { _, err := c.CheckLogs(context.Background(), "personal", "checkrun"); return err },
		MethodCheckWatch: func(c *Client) error { _, err := c.CheckWatch(context.Background(), CheckWatchParams{}); return err },
		MethodCheckRepairList: func(c *Client) error {
			_, err := c.CheckRepairList(context.Background(), CheckRepairQueryParams{})
			return err
		},
		MethodCheckRepairInspect: func(c *Client) error {
			_, err := c.CheckRepairInspect(context.Background(), "personal", "repair")
			return err
		},
		MethodCheckRepairAccept: func(c *Client) error {
			_, err := c.CheckRepairAccept(context.Background(), CheckRepairDecisionParams{})
			return err
		},
		MethodCheckRepairReject: func(c *Client) error {
			_, err := c.CheckRepairReject(context.Background(), CheckRepairDecisionParams{})
			return err
		},
	}
	if len(calls) != len(checkResultDiscriminators) {
		t.Fatalf("tested check clients = %d, discriminators = %d", len(calls), len(checkResultDiscriminators))
	}
	for method, call := range calls {
		expected := checkResultDiscriminators[method]
		err := captureCuratorRequestResultError(t, method, call, map[string]any{"schema": expected[0], "type": "wrong"})
		if err == nil || !strings.Contains(err.Error(), "discriminator") {
			t.Errorf("%s wrong discriminator error = %v", method, err)
		}
	}
}

func TestCheckClientsGenerateIdempotencyWithoutAuthorityInputs(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodCheckRun, func(client *Client) error {
		_, err := client.CheckRun(context.Background(), CheckRunParams{
			Workspace: "personal", Task: "task_exact", Definition: "unit",
			ExpectedRequirementRevision: 2, ExpectedDefinitionContentRevision: 3,
		})
		return err
	}, CheckRunMutationResult{Schema: CheckRunMutationSchema, Type: "check_run_mutation"})
	var params CheckRunParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(params.IdempotencyKey) == "" || params.ExpectedRequirementRevision != 2 || params.ExpectedDefinitionContentRevision != 3 {
		t.Fatalf("check run params = %#v", params)
	}
	for _, forbidden := range []string{"actor", "agent", "role", "purpose", "environment", "stdin", "arguments", "executable"} {
		if strings.Contains(string(request.Params), `"`+forbidden+`"`) {
			t.Errorf("check run exposed caller-selected %q: %s", forbidden, request.Params)
		}
	}
}

func TestCheckDefinitionClientEncodesZeroArgumentsAsArray(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodCheckDefinitionCreate, func(client *Client) error {
		_, err := client.CheckDefinitionCreate(context.Background(), CheckDefinitionCreateParams{Workspace: "personal", Project: "demo", Name: "unit"})
		return err
	}, CheckDefinitionMutationResult{Schema: CheckDefinitionMutationSchema, Type: "check_definition_mutation"})
	if !strings.Contains(string(request.Params), `"arguments":[]`) || strings.Contains(string(request.Params), `"arguments":null`) {
		t.Fatalf("definition request does not preserve an empty argv: %s", request.Params)
	}
}

func TestCheckClientRejectsUnknownResultFields(t *testing.T) {
	t.Parallel()
	err := captureCuratorRequestResultError(t, MethodCheckLogs, func(client *Client) error {
		_, err := client.CheckLogs(context.Background(), "personal", "checkrun_exact")
		return err
	}, map[string]any{
		"schema":               CheckLogsSchema,
		"type":                 "check_run_logs",
		"logs":                 map[string]any{"check_run_id": "checkrun_exact"},
		"raw_runtime_snapshot": "forbidden",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("check logs unknown result error = %v", err)
	}
}
