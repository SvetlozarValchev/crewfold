package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"crewfold/internal/domain"
)

const maximumDomainAgentToolResponseBytes = 256 * 1024

type DomainAgentToolReceiptCommand struct {
	ThreadID  string
	CallID    string
	TurnID    string
	ToolName  string
	Arguments json.RawMessage
}

// ReplayDomainAgentToolReceipt returns the first durably committed response for
// one exact provider call. The private thread binding is re-authorized on every
// lookup; a copied call id never grants cross-agent or cross-node access.
func (s *Store) ReplayDomainAgentToolReceipt(ctx context.Context, command DomainAgentToolReceiptCommand) (domain.DomainAgentToolReceipt, bool, error) {
	requestSHA, err := validateDomainAgentToolReceiptCommand(command)
	if err != nil {
		return domain.DomainAgentToolReceipt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.DomainAgentToolReceipt{}, false, storageFailure("begin domain agent tool receipt replay", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, strings.TrimSpace(command.ThreadID))
	if err != nil {
		return domain.DomainAgentToolReceipt{}, false, err
	}
	receipt, found, err := queryDomainAgentToolReceipt(ctx, tx, scope.Project.ID, scope.Agent.ID, strings.TrimSpace(command.CallID))
	if err != nil || !found {
		return receipt, found, err
	}
	if receipt.RequestSHA256 != requestSHA || receipt.TurnID != strings.TrimSpace(command.TurnID) || receipt.ToolName != strings.TrimSpace(command.ToolName) || receipt.SessionRevision != scope.Session.Revision {
		return domain.DomainAgentToolReceipt{}, false, &Error{Code: CodeDomainAgentToolConflict, Message: "provider tool call id was already used with different content"}
	}
	return receipt, true, nil
}

// RecordDomainAgentToolReceipt commits one bounded Crewfold-owned response.
// Exact replay returns the first response; divergent reuse fails closed.
func (s *Store) RecordDomainAgentToolReceipt(ctx context.Context, command DomainAgentToolReceiptCommand, response any, succeeded bool) (domain.DomainAgentToolReceipt, error) {
	requestSHA, err := validateDomainAgentToolReceiptCommand(command)
	if err != nil {
		return domain.DomainAgentToolReceipt{}, err
	}
	responseJSON, err := canonicalEventDataJSON(response)
	if err != nil || len(responseJSON) == 0 || len(responseJSON) > maximumDomainAgentToolResponseBytes {
		return domain.DomainAgentToolReceipt{}, &Error{Code: CodeInvalidDomainAgentTool, Message: "durable agent tool response must be one bounded JSON object", Cause: err}
	}
	var responseObject map[string]any
	if err := json.Unmarshal(responseJSON, &responseObject); err != nil {
		return domain.DomainAgentToolReceipt{}, &Error{Code: CodeInvalidDomainAgentTool, Message: "durable agent tool response is invalid", Cause: err}
	}
	responseSuccess, ok := responseObject["success"].(bool)
	if !ok || responseSuccess != succeeded {
		return domain.DomainAgentToolReceipt{}, &Error{Code: CodeInvalidDomainAgentTool, Message: "durable agent tool response status is inconsistent"}
	}
	responseDigest := sha256.Sum256(responseJSON)
	responseSHA := hex.EncodeToString(responseDigest[:])
	status := "failed"
	if succeeded {
		status = "succeeded"
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.DomainAgentToolReceipt{}, storageFailure("begin domain agent tool receipt", err)
	}
	defer tx.Rollback()
	scope, err := s.domainAgentSessionScopeInTransaction(ctx, tx, strings.TrimSpace(command.ThreadID))
	if err != nil {
		return domain.DomainAgentToolReceipt{}, err
	}
	existing, found, err := queryDomainAgentToolReceipt(ctx, tx, scope.Project.ID, scope.Agent.ID, strings.TrimSpace(command.CallID))
	if err != nil {
		return domain.DomainAgentToolReceipt{}, err
	}
	if found {
		if existing.RequestSHA256 != requestSHA || existing.TurnID != strings.TrimSpace(command.TurnID) || existing.ToolName != strings.TrimSpace(command.ToolName) || existing.SessionRevision != scope.Session.Revision {
			return domain.DomainAgentToolReceipt{}, &Error{Code: CodeDomainAgentToolConflict, Message: "provider tool call id was already used with different content"}
		}
		return existing, nil
	}
	receiptID, err := randomID("toolrcpt_")
	if err != nil {
		return domain.DomainAgentToolReceipt{}, storageFailure("generate domain agent tool receipt id", err)
	}
	receipt := domain.DomainAgentToolReceipt{
		ID: receiptID, ProjectID: scope.Project.ID, AgentID: scope.Agent.ID, SessionRevision: scope.Session.Revision,
		CallID: strings.TrimSpace(command.CallID), TurnID: strings.TrimSpace(command.TurnID), ToolName: strings.TrimSpace(command.ToolName),
		RequestSHA256: requestSHA, Status: status, ResponseSHA256: responseSHA, Response: append(json.RawMessage(nil), responseJSON...), CreatedAt: s.nowText(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_agent_tool_receipts(
id,project_id,agent_id,session_revision,call_id,turn_id,tool_name,request_sha256,status,response_sha256,response_json,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.ID, receipt.ProjectID, receipt.AgentID, receipt.SessionRevision,
		receipt.CallID, receipt.TurnID, receipt.ToolName, receipt.RequestSHA256, receipt.Status,
		receipt.ResponseSHA256, string(receipt.Response), receipt.CreatedAt); err != nil {
		return domain.DomainAgentToolReceipt{}, storageFailure("insert domain agent tool receipt", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.DomainAgentToolReceipt{}, storageFailure("commit domain agent tool receipt", err)
	}
	return receipt, nil
}

func validateDomainAgentToolReceiptCommand(command DomainAgentToolReceiptCommand) (string, error) {
	threadID, callID, turnID, toolName := strings.TrimSpace(command.ThreadID), strings.TrimSpace(command.CallID), strings.TrimSpace(command.TurnID), strings.TrimSpace(command.ToolName)
	if !validPrivateSessionText(threadID, maximumDomainAgentThreadIDBytes) || !validPrivateSessionText(callID, 512) || !validPrivateSessionText(turnID, 512) || !validDomainAgentToolName(toolName) {
		return "", &Error{Code: CodeInvalidDomainAgentTool, Message: "durable agent tool receipt identifiers are invalid"}
	}
	arguments, err := canonicalDomainAgentToolArguments(command.Arguments)
	if err != nil {
		return "", &Error{Code: CodeInvalidDomainAgentTool, Message: "durable agent tool arguments must be one JSON object", Cause: err}
	}
	return hashCommand("domain-agent-tool-call", map[string]any{"turn_id": turnID, "tool_name": toolName, "arguments": json.RawMessage(arguments)})
}

func canonicalDomainAgentToolArguments(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil || arguments == nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("tool arguments contain trailing JSON")
	}
	return json.Marshal(arguments)
}

func validDomainAgentToolName(value string) bool {
	if !validPrivateSessionText(value, 128) || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func queryDomainAgentToolReceipt(ctx context.Context, query queryRower, projectID, agentID, callID string) (domain.DomainAgentToolReceipt, bool, error) {
	var receipt domain.DomainAgentToolReceipt
	var response string
	err := query.QueryRowContext(ctx, `SELECT id,project_id,agent_id,session_revision,call_id,turn_id,tool_name,request_sha256,status,response_sha256,response_json,created_at
FROM domain_agent_tool_receipts WHERE project_id=? AND agent_id=? AND call_id=?`, projectID, agentID, callID).Scan(
		&receipt.ID, &receipt.ProjectID, &receipt.AgentID, &receipt.SessionRevision, &receipt.CallID, &receipt.TurnID,
		&receipt.ToolName, &receipt.RequestSHA256, &receipt.Status, &receipt.ResponseSHA256, &response, &receipt.CreatedAt)
	if err == nil {
		receipt.Response = json.RawMessage(response)
		return receipt, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DomainAgentToolReceipt{}, false, nil
	}
	return domain.DomainAgentToolReceipt{}, false, storageFailure("query domain agent tool receipt", err)
}
