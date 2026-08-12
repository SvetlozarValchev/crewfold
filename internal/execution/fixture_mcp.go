package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

// FixtureMCPProvider is the first provider-neutral direct subprocess that uses
// Crewfold's scoped MCP capability instead of encoding reports in stdout.
type FixtureMCPProvider struct {
	preparer   RunCapabilityPreparer
	executable string
	arguments  []string
}

func NewFixtureMCPProvider(preparer RunCapabilityPreparer) FixtureMCPProvider {
	executable, _ := os.Executable()
	return FixtureMCPProvider{preparer: preparer, executable: executable, arguments: []string{"__fixture-mcp-provider"}}
}

func (FixtureMCPProvider) Name() string { return "fixture-mcp" }

func (provider FixtureMCPProvider) Prepare(ctx context.Context, run domain.Run, scenario domain.FakeScenario) (LaunchSpec, error) {
	if err := ValidateScenario(scenario); err != nil {
		return LaunchSpec{}, err
	}
	if scenario.StartFailure != "" {
		return LaunchSpec{}, &StartError{Message: scenario.StartFailure}
	}
	if provider.preparer == nil || strings.TrimSpace(provider.executable) == "" {
		return LaunchSpec{}, errors.New("scoped fixture provider is unavailable")
	}
	access, err := provider.preparer.PrepareRunCapability(ctx, run.ID)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("prepare scoped run capability: %w", err)
	}
	input, err := json.Marshal(scenario)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("encode fixture scenario: %w", err)
	}
	command := &CommandSpec{
		Executable: provider.executable, Arguments: append([]string(nil), provider.arguments...),
		StandardInput: input,
		Environment: map[string]string{
			"CREWFOLD_MCP_SOCKET":          access.SocketPath,
			"CREWFOLD_MCP_CAPABILITY_FILE": access.CapabilityFile,
		},
		Timeout: time.Duration(scenario.Process.TimeoutMillis) * time.Millisecond, OutputByteLimit: 64 * 1024,
	}
	return LaunchSpec{Scenario: scenario, Command: command}, nil
}

func (FixtureMCPProvider) Bind(_ context.Context, run domain.Run, binding RuntimeBinding) (ProviderBinding, error) {
	if binding.RuntimeHandle == "" {
		return ProviderBinding{}, errors.New("cannot bind scoped fixture provider without a runtime handle")
	}
	return ProviderBinding{ProviderHandle: "fixture-mcp-provider:" + run.ID}, nil
}

func (FixtureMCPProvider) Next(_ context.Context, _ domain.Run, scenario domain.FakeScenario, _ RuntimeSnapshot) (domain.RunObservation, bool, error) {
	if err := ValidateScenario(scenario); err != nil {
		return domain.RunObservation{}, false, err
	}
	return domain.RunObservation{}, false, nil
}

func RunFixtureMCPProvider(input io.Reader, output, diagnostics io.Writer) int {
	scenario, err := readFixtureScenario(input)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	configureFixtureSignals(scenario.Process.IgnoreTermination)
	writeFixtureMetadata(output)

	tokenBytes, err := os.ReadFile(os.Getenv("CREWFOLD_MCP_CAPABILITY_FILE"))
	if err != nil || len(tokenBytes) > 1024 || strings.TrimSpace(string(tokenBytes)) == "" {
		fmt.Fprintln(diagnostics, "read scoped capability file failed")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	client := &fixtureMCPConnection{socketPath: os.Getenv("CREWFOLD_MCP_SOCKET"), token: strings.TrimSpace(string(tokenBytes))}
	err = client.connect(ctx)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	defer client.Close()
	briefingResult, err := client.CallTool(ctx, "crewfold_get_briefing", map[string]any{})
	if err != nil || briefingResult.IsError {
		fmt.Fprintln(diagnostics, "get scoped briefing failed")
		return 1
	}
	if scenario.Mailbox.RequireInboxSummary {
		var briefing domain.RunBriefing
		if err := json.Unmarshal(briefingResult.StructuredContent, &briefing); err != nil || briefing.Packet.Inbox.UnseenCount < 1 || len(briefing.Packet.Inbox.Items) < 1 {
			fmt.Fprintln(diagnostics, "scoped briefing omitted required inbox summary")
			return 1
		}
	}
	if scenario.Process.CrossRunProbe {
		_, err := client.ReadResource(ctx, "crewfold://runs/run_ffffffffffffffffffffffffffffffff/briefing")
		var rpcError *mcp.RPCError
		if !errors.As(err, &rpcError) || rpcError.Data == nil || rpcError.Data.Code != "out_of_scope" {
			fmt.Fprintln(diagnostics, "cross-run resource probe was not denied")
			return 1
		}
	}
	if scenario.Process.PublishArtifact {
		result, err := client.CallTool(ctx, "crewfold_publish_artifact", map[string]any{
			"name": "fixture-evidence", "media_type": "text/plain", "content": "scoped fixture evidence", "idempotency_key": "fixture-artifact",
		})
		if err != nil || result.IsError {
			fmt.Fprintln(diagnostics, "publish scoped artifact failed")
			return 1
		}
	}
	if err := runFixtureMailbox(ctx, client, scenario.Mailbox); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	for index, step := range scenario.Steps {
		result, err := reportFixtureStep(ctx, client, index, step)
		if err != nil || result.IsError {
			fmt.Fprintf(diagnostics, "report fixture step %d failed\n", index)
			return 1
		}
		if scenario.Process.DuplicateReport && index == 0 {
			duplicate, duplicateErr := reportFixtureStep(ctx, client, index, step)
			var originalReport, duplicateReport domain.RunReport
			originalErr := json.Unmarshal(result.StructuredContent, &originalReport)
			duplicateDecodeErr := json.Unmarshal(duplicate.StructuredContent, &duplicateReport)
			if duplicateErr != nil || duplicate.IsError || originalErr != nil || duplicateDecodeErr != nil || originalReport.ID == "" || originalReport.ID != duplicateReport.ID {
				fmt.Fprintln(diagnostics, "duplicate fixture report was not idempotent")
				return 1
			}
		}
		if scenario.Process.StepDelayMillis > 0 {
			time.Sleep(time.Duration(scenario.Process.StepDelayMillis) * time.Millisecond)
		}
	}
	writeNoise(output, 'x', scenario.Process.StdoutNoiseBytes)
	writeNoise(diagnostics, 'e', scenario.Process.StderrNoiseBytes)
	if scenario.Process.HangAfterSteps {
		for {
			time.Sleep(time.Hour)
		}
	}
	return scenario.Process.ExitCode
}

type fixtureToolClient interface {
	CallTool(context.Context, string, any) (mcp.ToolCallResult, error)
	ReadResource(context.Context, string) ([]mcp.ResourceContents, error)
}

type fixtureMCPConnection struct {
	socketPath string
	token      string
	client     *mcp.Client
}

func (connection *fixtureMCPConnection) connect(ctx context.Context) error {
	client, err := mcp.Dial(ctx, connection.socketPath, connection.token)
	if err != nil {
		return err
	}
	if err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		return err
	}
	connection.client = client
	return nil
}

func (connection *fixtureMCPConnection) Close() error {
	if connection.client == nil {
		return nil
	}
	err := connection.client.Close()
	connection.client = nil
	return err
}

func (connection *fixtureMCPConnection) CallTool(ctx context.Context, name string, arguments any) (mcp.ToolCallResult, error) {
	for {
		if connection.client == nil {
			if err := connection.reconnect(ctx); err != nil {
				return mcp.ToolCallResult{}, err
			}
		}
		result, err := connection.client.CallTool(ctx, name, arguments)
		if err == nil || !retryableFixtureMCPError(err) {
			return result, err
		}
		_ = connection.Close()
	}
}

func (connection *fixtureMCPConnection) ReadResource(ctx context.Context, uri string) ([]mcp.ResourceContents, error) {
	for {
		if connection.client == nil {
			if err := connection.reconnect(ctx); err != nil {
				return nil, err
			}
		}
		result, err := connection.client.ReadResource(ctx, uri)
		if err == nil || !retryableFixtureMCPError(err) {
			return result, err
		}
		_ = connection.Close()
	}
}

func (connection *fixtureMCPConnection) reconnect(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := connection.connect(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func retryableFixtureMCPError(err error) bool {
	var rpcError *mcp.RPCError
	return !errors.As(err, &rpcError)
}

func runFixtureMailbox(ctx context.Context, client fixtureToolClient, plan domain.FixtureMailbox) error {
	if plan == (domain.FixtureMailbox{}) {
		return nil
	}
	if plan.DeniedRecipientProbe != "" {
		result, err := client.CallTool(ctx, "crewfold_send_message", map[string]any{
			"recipient_agent": plan.DeniedRecipientProbe, "kind": domain.MessageInform,
			"subject": "Denied recipient probe", "body": "This message must be denied.",
			"artifact_ids": []string{}, "idempotency_key": "fixture-denied-recipient",
		})
		if err != nil || !result.IsError || fixtureToolErrorCode(result) != "denied_by_policy" {
			return errors.New("unauthorized message recipient probe was not denied")
		}
	}
	if plan.OversizedRecipientProbe != "" {
		result, err := client.CallTool(ctx, "crewfold_send_message", map[string]any{
			"recipient_agent": plan.OversizedRecipientProbe, "kind": domain.MessageInform,
			"subject": "Oversized message probe", "body": strings.Repeat("x", 4097),
			"artifact_ids": []string{}, "idempotency_key": "fixture-oversized-message",
		})
		if err != nil || !result.IsError || fixtureToolErrorCode(result) != "invalid_input" {
			return errors.New("oversized message probe was not rejected")
		}
	}
	threadID := ""
	if plan.Send != nil {
		mutation, err := sendFixtureMessage(ctx, client, *plan.Send, "", "", "fixture-mail-send")
		if err != nil {
			return err
		}
		threadID = mutation.Thread.ID
	}
	if plan.WaitForKind == "" {
		return nil
	}
	wait := time.Duration(plan.WaitTimeoutMillis) * time.Millisecond
	if wait == 0 {
		wait = 10 * time.Second
	}
	deadline := time.Now().Add(wait)
	var incoming domain.InboxItem
	for time.Now().Before(deadline) {
		result, err := client.CallTool(ctx, "crewfold_list_inbox", map[string]any{"limit": 50})
		if err != nil {
			return fmt.Errorf("list fixture inbox: %w", err)
		}
		if result.IsError {
			return errors.New("list fixture inbox was denied")
		}
		var items []domain.InboxItem
		if err := json.Unmarshal(result.StructuredContent, &items); err != nil {
			return fmt.Errorf("decode fixture inbox: %w", err)
		}
		for _, item := range items {
			if item.Message.Kind == plan.WaitForKind && (threadID == "" || item.Message.ThreadID == threadID) {
				incoming = item
				break
			}
		}
		if incoming.Message.ID != "" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	if incoming.Message.ID == "" {
		return fmt.Errorf("timed out waiting for fixture mailbox kind %q", plan.WaitForKind)
	}
	read, err := client.CallTool(ctx, "crewfold_read_message", map[string]any{"message_id": incoming.Message.ID, "idempotency_key": "fixture-read-" + incoming.Message.ID})
	if err != nil || read.IsError {
		return errors.New("read fixture message failed")
	}
	if plan.AcknowledgeReceived {
		acknowledged, err := client.CallTool(ctx, "crewfold_acknowledge_message", map[string]any{"message_id": incoming.Message.ID, "idempotency_key": "fixture-ack-" + incoming.Message.ID})
		if err != nil || acknowledged.IsError {
			return errors.New("acknowledge fixture message failed")
		}
	}
	if plan.Reply != nil {
		reply := *plan.Reply
		reply.RecipientAgent = incoming.Message.SenderAgentID
		if _, err := sendFixtureMessage(ctx, client, reply, incoming.Message.ThreadID, incoming.Message.ID, "fixture-mail-reply"); err != nil {
			return err
		}
	}
	return nil
}

func sendFixtureMessage(ctx context.Context, client fixtureToolClient, message domain.FixtureMailboxMessage, threadID, replyTo, key string) (domain.MessageMutation, error) {
	arguments := map[string]any{
		"recipient_agent": message.RecipientAgent, "kind": message.Kind, "body": message.Body,
		"artifact_ids": []string{}, "idempotency_key": key,
	}
	if message.Subject != "" {
		arguments["subject"] = message.Subject
	}
	if threadID != "" {
		arguments["thread_id"] = threadID
	}
	if replyTo != "" {
		arguments["reply_to_message_id"] = replyTo
	}
	result, err := client.CallTool(ctx, "crewfold_send_message", arguments)
	if err != nil || result.IsError {
		return domain.MessageMutation{}, errors.New("send fixture message failed")
	}
	var mutation domain.MessageMutation
	if err := json.Unmarshal(result.StructuredContent, &mutation); err != nil || mutation.Message.ID == "" {
		return domain.MessageMutation{}, errors.New("decode fixture message result failed")
	}
	return mutation, nil
}

func fixtureToolErrorCode(result mcp.ToolCallResult) string {
	var body mcp.ToolError
	_ = json.Unmarshal(result.StructuredContent, &body)
	return body.Code
}

func reportFixtureStep(ctx context.Context, client fixtureToolClient, index int, step domain.FakeStep) (mcp.ToolCallResult, error) {
	key := fmt.Sprintf("fixture-step-%d", index)
	switch step.Kind {
	case domain.ObservationProgress:
		return client.CallTool(ctx, "crewfold_report_progress", map[string]any{
			"summary": step.Message, "completed": []string{step.Message}, "next": []string{}, "risks": []string{}, "evidence_ids": step.Evidence, "idempotency_key": key,
		})
	case domain.ObservationBlocked:
		return client.CallTool(ctx, "crewfold_report_blocked", map[string]any{
			"reason": step.Message, "needs": []string{"owner resolution"}, "severity": "blocking", "related_ids": step.Evidence, "idempotency_key": key,
		})
	case domain.ObservationCompletion:
		return client.CallTool(ctx, "crewfold_propose_completion", map[string]any{
			"summary": step.Message, "handoff": step.Handoff, "evidence_ids": step.Evidence,
			"changed_paths": []string{}, "checks": []string{}, "remaining_risks": []string{}, "unknowns": []string{}, "idempotency_key": key,
		})
	default:
		return mcp.ToolCallResult{}, fmt.Errorf("unsupported fixture step %q", step.Kind)
	}
}

func readFixtureScenario(input io.Reader) (domain.FakeScenario, error) {
	data, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return domain.FakeScenario{}, errors.New("fixture input is unreadable or exceeds 64 KiB")
	}
	var scenario domain.FakeScenario
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenario); err != nil {
		return domain.FakeScenario{}, fmt.Errorf("decode fixture input: %w", err)
	}
	if err := ValidateScenario(scenario); err != nil {
		return domain.FakeScenario{}, fmt.Errorf("validate fixture input: %w", err)
	}
	return scenario, nil
}

func configureFixtureSignals(ignoreTermination bool) {
	if !ignoreTermination {
		return
	}
	ignored := make(chan os.Signal, 4)
	signal.Notify(ignored, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range ignored {
		}
	}()
}

func writeFixtureMetadata(output io.Writer) {
	workingDirectory, _ := os.Getwd()
	environmentNames := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if name, _, ok := strings.Cut(entry, "="); ok {
			environmentNames = append(environmentNames, name)
		}
	}
	sort.Strings(environmentNames)
	_ = json.NewEncoder(output).Encode(fixtureRuntimeMetadata{Schema: fixtureRuntimeMetadataSchema, WorkingDirectory: workingDirectory, EnvironmentNames: environmentNames})
}
