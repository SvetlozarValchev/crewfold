package localapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestManagementResultDiscriminatorsRejectWrongSchemaAndType(t *testing.T) {
	t.Parallel()
	type result struct {
		Schema string
		Type   string
	}
	if len(managementResultDiscriminators) != 23 {
		t.Fatalf("management discriminator method count = %d, want 23", len(managementResultDiscriminators))
	}
	for method, expected := range managementResultDiscriminators {
		method, expected := method, expected
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			if err := validateManagementResultDiscriminator(method, &result{Schema: expected[0], Type: expected[1]}); err != nil {
				t.Fatalf("valid discriminator rejected: %v", err)
			}
			for name, value := range map[string]result{
				"schema": {Schema: "urn:wrong", Type: expected[1]},
				"type":   {Schema: expected[0], Type: "wrong"},
			} {
				if err := validateManagementResultDiscriminator(method, &value); err == nil || !strings.Contains(err.Error(), "discriminator") {
					t.Errorf("%s mismatch error = %v, want discriminator failure", name, err)
				}
			}
		})
	}
}

func TestManagementClientsRejectWrongResultDiscriminators(t *testing.T) {
	t.Parallel()
	calls := map[string]func(*Client) error{
		MethodManagerGrantCreate: func(client *Client) error {
			_, err := client.ManagerGrantCreate(context.Background(), ManagerGrantCreateParams{})
			return err
		},
		MethodManagerGrantRevoke: func(client *Client) error {
			_, err := client.ManagerGrantRevoke(context.Background(), ManagerGrantRevokeParams{})
			return err
		},
		MethodManagerGrantShow: func(client *Client) error {
			_, err := client.ManagerGrantShow(context.Background(), "personal", "mgrgrant_exact")
			return err
		},
		MethodManagerGrantList: func(client *Client) error {
			_, err := client.ManagerGrantList(context.Background(), ManagerGrantQueryParams{})
			return err
		},
		MethodLaunchProfileCreate: func(client *Client) error {
			_, err := client.LaunchProfileCreate(context.Background(), LaunchProfileCreateParams{})
			return err
		},
		MethodLaunchProfileRetire: func(client *Client) error {
			_, err := client.LaunchProfileRetire(context.Background(), LaunchProfileRetireParams{})
			return err
		},
		MethodLaunchProfileShow: func(client *Client) error {
			_, err := client.LaunchProfileShow(context.Background(), "personal", "lprof_exact")
			return err
		},
		MethodLaunchProfileList: func(client *Client) error {
			_, err := client.LaunchProfileList(context.Background(), LaunchProfileQueryParams{})
			return err
		},
		MethodManagerInvoke: func(client *Client) error {
			_, err := client.ManagerInvoke(context.Background(), ManagerInvokeParams{})
			return err
		},
		MethodProposalList: func(client *Client) error {
			_, err := client.ProposalList(context.Background(), ProposalQueryParams{})
			return err
		},
		MethodProposalInspect: func(client *Client) error {
			_, err := client.ProposalInspect(context.Background(), "personal", "mprop_exact")
			return err
		},
		MethodProposalAccept: func(client *Client) error {
			_, err := client.ProposalAccept(context.Background(), ProposalDecisionParams{})
			return err
		},
		MethodProposalReject: func(client *Client) error {
			_, err := client.ProposalReject(context.Background(), ProposalDecisionParams{})
			return err
		},
		MethodSupervisorPolicyShow: func(client *Client) error {
			_, err := client.SupervisorPolicyShow(context.Background(), "personal")
			return err
		},
		MethodSupervisorPolicyConfigure: func(client *Client) error {
			_, err := client.SupervisorPolicyConfigure(context.Background(), SupervisorPolicyConfigureParams{})
			return err
		},
		MethodSupervisorRun: func(client *Client) error {
			_, err := client.SupervisorRun(context.Background(), SupervisorRunParams{})
			return err
		},
		MethodSupervisorActionList: func(client *Client) error {
			_, err := client.SupervisorActionList(context.Background(), SupervisorActionQueryParams{})
			return err
		},
		MethodSupervisorActionShow: func(client *Client) error {
			_, err := client.SupervisorActionShow(context.Background(), "personal", "saction_exact")
			return err
		},
		MethodSupervisorExplain: func(client *Client) error {
			_, err := client.SupervisorExplain(context.Background(), SupervisorExplainParams{})
			return err
		},
		MethodApprovalList: func(client *Client) error {
			_, err := client.ApprovalList(context.Background(), ApprovalQueryParams{})
			return err
		},
		MethodApprovalInspect: func(client *Client) error {
			_, err := client.ApprovalInspect(context.Background(), "personal", "appr_exact")
			return err
		},
		MethodApprovalAllow: func(client *Client) error {
			_, err := client.ApprovalAllow(context.Background(), ApprovalDecisionParams{})
			return err
		},
		MethodApprovalDeny: func(client *Client) error {
			_, err := client.ApprovalDeny(context.Background(), ApprovalDecisionParams{})
			return err
		},
	}
	if len(calls) != len(managementResultDiscriminators) {
		t.Fatalf("tested management clients = %d, discriminators = %d", len(calls), len(managementResultDiscriminators))
	}
	for method, call := range calls {
		expected, exists := managementResultDiscriminators[method]
		if !exists {
			t.Fatalf("client method %q has no discriminator", method)
		}
		method, expected := method, expected
		call := call
		for _, test := range []struct {
			name   string
			result map[string]any
		}{
			{name: "schema", result: map[string]any{"schema": "urn:wrong", "type": expected[1]}},
			{name: "type", result: map[string]any{"schema": expected[0], "type": "wrong"}},
		} {
			test := test
			t.Run(method+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				err := captureCuratorRequestResultError(t, method, call, test.result)
				if err == nil || !strings.Contains(err.Error(), "discriminator") {
					t.Fatalf("wrong discriminator error = %v", err)
				}
			})
		}
	}
}

func TestContextShowResultRequiresTheCurrentPacketSchema(t *testing.T) {
	t.Parallel()
	value := ContextShowResult{Schema: ContextShowSchema, Type: "context_packet", Packet: domain.ContextPacket{Schema: domain.ContextPacketSchema}}
	if err := validateContextShowResult(value); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	value.Schema = "urn:wrong"
	if err := validateContextShowResult(value); err == nil {
		t.Fatal("wrong result schema accepted")
	}
	value.Schema, value.Type = ContextShowSchema, "wrong"
	if err := validateContextShowResult(value); err == nil {
		t.Fatal("wrong type accepted")
	}
	value.Type, value.Packet.Schema = "context_packet", "urn:example:invalid-context-packet"
	if err := validateContextShowResult(value); err == nil {
		t.Fatal("noncurrent packet schema accepted")
	}
}

func TestContextShowAndExplainClientsRejectWrongDiscriminators(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		method string
		call   func(*Client) error
		result any
	}{
		{
			name: "show schema", method: MethodContextShow,
			call: func(client *Client) error {
				_, err := client.ContextShow(context.Background(), "personal", "ctx_exact")
				return err
			},
			result: ContextShowResult{Schema: "urn:wrong", Type: "context_packet", Packet: domain.ContextPacket{Schema: domain.ContextPacketSchema}},
		},
		{
			name: "show accepts current schema", method: MethodContextShow,
			call: func(client *Client) error {
				result, err := client.ContextShow(context.Background(), "personal", "ctx_exact")
				if err != nil {
					return err
				}
				if result.Schema != ContextShowSchema || result.Packet.Schema != domain.ContextPacketSchema {
					return fmt.Errorf("unexpected decoded current context result")
				}
				return nil
			},
			result: ContextShowResult{Schema: ContextShowSchema, Type: "context_packet", Packet: domain.ContextPacket{Schema: domain.ContextPacketSchema}},
		},
		{
			name: "explain type", method: MethodContextExplain,
			call: func(client *Client) error {
				_, err := client.ContextExplain(context.Background(), "personal", "ctx_exact")
				return err
			},
			result: ContextExplainResult{Schema: ContextExplainSchema, Type: "wrong"},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := captureCuratorRequestResultError(t, test.method, test.call, test.result)
			if test.name == "show accepts current schema" {
				if err != nil {
					t.Fatalf("current context.show error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "unexpected result schema") {
				t.Fatalf("wrong discriminator error = %v", err)
			}
		})
	}
}
