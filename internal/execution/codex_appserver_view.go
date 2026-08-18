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
			item, ok := readableCodexItem(raw)
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

func readableCodexItem(raw json.RawMessage) (domain.DomainAgentSessionItem, bool) {
	var envelope struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregatedOutput"`
		Status           string `json:"status"`
		Tool             string `json:"tool"`
		Content          []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ID == "" || envelope.Type == "" {
		return domain.DomainAgentSessionItem{}, false
	}
	item := domain.DomainAgentSessionItem{ID: boundedCodexText(envelope.ID), Type: boundedCodexText(envelope.Type), Status: boundedCodexText(envelope.Status)}
	switch envelope.Type {
	case "userMessage":
		parts := make([]string, 0, len(envelope.Content))
		for _, content := range envelope.Content {
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
	case "dynamicToolCall", "collabAgentToolCall":
		item.Command = boundedCodexText(envelope.Tool)
	case "fileChange", "reasoning":
		// These types are useful as structured lifecycle markers but their rich
		// payloads belong in Changes or the advanced provider console.
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
