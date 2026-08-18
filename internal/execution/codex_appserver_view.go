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
		Server           string `json:"server"`
		Query            string `json:"query"`
		Prompt           string `json:"prompt"`
		Kind             string `json:"kind"`
		Tool             string `json:"tool"`
		Error            *struct {
			Message string `json:"message"`
		} `json:"error"`
		Changes []struct {
			Path string `json:"path"`
			Kind struct {
				Type string `json:"type"`
			} `json:"kind"`
		} `json:"changes"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ID == "" || envelope.Type == "" {
		return domain.DomainAgentSessionItem{}, false
	}
	item := domain.DomainAgentSessionItem{ID: boundedCodexText(envelope.ID), Type: boundedCodexText(envelope.Type), Status: boundedCodexText(envelope.Status)}
	switch envelope.Type {
	case "userMessage":
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
	case "mcpToolCall":
		item.Command = boundedCodexText(strings.Trim(strings.Join([]string{envelope.Server, envelope.Tool}, "."), "."))
		if envelope.Error != nil {
			item.Text = boundedCodexText(envelope.Error.Message)
		}
	case "dynamicToolCall":
		item.Command = boundedCodexText(envelope.Tool)
	case "collabAgentToolCall":
		item.Command = boundedCodexText(envelope.Tool)
		item.Text = boundedCodexText(envelope.Prompt)
	case "fileChange":
		changes := make([]string, 0, len(envelope.Changes))
		for _, change := range envelope.Changes {
			label := strings.TrimSpace(strings.Join([]string{change.Kind.Type, change.Path}, " "))
			if label != "" {
				changes = append(changes, label)
			}
		}
		item.Text = boundedCodexText(strings.Join(changes, "\n"))
	case "webSearch":
		item.Command = boundedCodexText(envelope.Query)
	case "subAgentActivity":
		item.Command = boundedCodexText(envelope.Kind)
	case "reasoning":
		// Reasoning is shown only as a lifecycle marker. Private reasoning text
		// and summaries are not part of Crewfold's observable work record.
	default:
		return domain.DomainAgentSessionItem{}, false
	}
	return item, true
}

func boundedCodexText(value string) string {
	if len([]byte(value)) <= maximumReadableCodexItemBytes {
		return value
	}
	data := []byte(value)[:maximumReadableCodexItemBytes]
	for len(data) != 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}
