package execution

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
)

const (
	maximumReadableCodexTurns     = 100
	maximumReadableCodexItems     = 512
	maximumReadableCodexItemBytes = 64 * 1024
	maximumReadableCommandActions = 64
	maximumReadableFileChanges    = 128
)

// ReadableCodexTurns converts the provider's persisted structured thread items
// into a bounded display projection. It never parses terminal bytes or invents
// Crewfold state from provider prose.
func ReadableCodexTurns(thread CodexThread) []domain.DomainAgentSessionTurn {
	turns := thread.Turns
	if len(turns) > maximumReadableCodexTurns {
		turns = turns[len(turns)-maximumReadableCodexTurns:]
	}
	result := make([]domain.DomainAgentSessionTurn, 0, len(turns))
	remaining := maximumReadableCodexItems
	for _, turn := range turns {
		readable := domain.DomainAgentSessionTurn{ID: boundedCodexText(turn.ID), Status: boundedCodexText(turn.Status), Items: []domain.DomainAgentSessionItem{}}
		for _, raw := range turn.Items {
			if remaining == 0 {
				break
			}
			item, ok := ReadableCodexItem(raw)
			if !ok {
				continue
			}
			readable.Items = append(readable.Items, item)
			remaining--
		}
		result = append(result, readable)
		if remaining == 0 {
			break
		}
	}
	return result
}

// ReadableCodexItem converts one exact app-server ThreadItem into the bounded
// owner-facing projection shared by persisted thread reads and live item
// notifications. It deliberately omits private reasoning text and raw tool
// arguments/results; those are neither necessary to understand observable
// progress nor Crewfold authority.
func ReadableCodexItem(raw json.RawMessage) (domain.DomainAgentSessionItem, bool) {
	var envelope struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregatedOutput"`
		Status           string `json:"status"`
		CWD              string `json:"cwd"`
		ProcessID        string `json:"processId"`
		ExitCode         *int   `json:"exitCode"`
		DurationMillis   int64  `json:"durationMs"`
		Server           string `json:"server"`
		Query            string `json:"query"`
		Prompt           string `json:"prompt"`
		Kind             string `json:"kind"`
		Tool             string `json:"tool"`
		ClientID         string `json:"clientId"`
		Error            *struct {
			Message string `json:"message"`
		} `json:"error"`
		Changes []struct {
			Path string `json:"path"`
			Diff string `json:"diff"`
			Kind struct {
				Type string `json:"type"`
			} `json:"kind"`
		} `json:"changes"`
		CommandActions []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Name    string `json:"name"`
			Path    string `json:"path"`
			Query   string `json:"query"`
		} `json:"commandActions"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ID == "" || envelope.Type == "" {
		return domain.DomainAgentSessionItem{}, false
	}
	item := domain.DomainAgentSessionItem{ID: boundedCodexText(envelope.ID), Type: boundedCodexText(envelope.Type), Status: boundedCodexText(envelope.Status)}
	switch envelope.Type {
	case "userMessage":
		item.Origin = "owner"
		if strings.HasPrefix(envelope.ClientID, "crewfold:wake:") {
			item.Origin = "crewfold_delivery"
		}
		var contentItems []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(envelope.Content, &contentItems) != nil {
			return domain.DomainAgentSessionItem{}, false
		}
		parts := make([]string, 0, len(contentItems))
		for _, content := range contentItems {
			if content.Type == "text" && content.Text != "" {
				parts = append(parts, content.Text)
			}
		}
		item.Text = boundedCodexText(strings.Join(parts, "\n"))
	case "agentMessage", "plan":
		item.Text = boundedCodexText(envelope.Text)
	case "commandExecution":
		item.Command = boundedCodexText(envelope.Command)
		item.Text = boundedCodexText(envelope.AggregatedOutput)
		item.CWD = boundedCodexText(envelope.CWD)
		item.ProcessID = boundedCodexText(envelope.ProcessID)
		item.ExitCode = envelope.ExitCode
		item.DurationMillis = max(envelope.DurationMillis, 0)
		for _, action := range envelope.CommandActions {
			if len(item.CommandActions) == maximumReadableCommandActions {
				break
			}
			if action.Type == "" {
				continue
			}
			item.CommandActions = append(item.CommandActions, domain.DomainAgentSessionCommandAction{
				Type: boundedCodexText(action.Type), Command: boundedCodexText(action.Command), Name: boundedCodexText(action.Name),
				Path: boundedCodexText(action.Path), Query: boundedCodexText(action.Query),
			})
		}
	case "mcpToolCall":
		item.Command = boundedCodexText(strings.Trim(strings.Join([]string{envelope.Server, envelope.Tool}, "."), "."))
		item.DurationMillis = max(envelope.DurationMillis, 0)
		if envelope.Error != nil {
			item.Text = boundedCodexText(envelope.Error.Message)
		}
	case "dynamicToolCall":
		item.Command = boundedCodexText(envelope.Tool)
		item.DurationMillis = max(envelope.DurationMillis, 0)
	case "collabAgentToolCall":
		item.Command = boundedCodexText(envelope.Tool)
		item.Text = boundedCodexText(envelope.Prompt)
	case "fileChange":
		remaining := maximumReadableCodexItemBytes
		for _, change := range envelope.Changes {
			if len(item.Changes) == maximumReadableFileChanges {
				break
			}
			label := strings.TrimSpace(strings.Join([]string{change.Kind.Type, change.Path}, " "))
			if label == "" {
				continue
			}
			diff := boundedCodexTextLimit(change.Diff, remaining)
			remaining -= len(diff)
			item.Changes = append(item.Changes, domain.DomainAgentSessionFileChange{
				Path: boundedCodexText(change.Path), Kind: boundedCodexText(change.Kind.Type), Diff: diff,
			})
		}
	case "webSearch":
		item.Command = boundedCodexText(envelope.Query)
	case "subAgentActivity":
		item.Command = boundedCodexText(envelope.Kind)
	case "reasoning":
		// Private reasoning is neither observable work nor canonical Crewfold
		// state. Omitting the provider lifecycle marker avoids inventing a
		// repetitive placeholder in the owner's activity stream.
		return domain.DomainAgentSessionItem{}, false
	default:
		return domain.DomainAgentSessionItem{}, false
	}
	return item, true
}

func boundedCodexText(value string) string {
	return boundedCodexTextLimit(value, maximumReadableCodexItemBytes)
}

func boundedCodexTextLimit(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len([]byte(value)) <= limit {
		return value
	}
	data := []byte(value)[:limit]
	for len(data) != 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}
