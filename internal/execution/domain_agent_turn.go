package execution

import "strings"

const (
	domainAgentRuntimeHandlePrefix = "domain-agent-thread:v1:"
	domainAgentTurnHandlePrefix    = "domain-agent-turn:v1:"
)

// DomainAgentRuntimeHandle binds a Crewfold execution run to the durable
// provider thread owned by the same domain agent. It is operational identity,
// never public output or canonical event data.
func DomainAgentRuntimeHandle(threadID string) string {
	return domainAgentRuntimeHandlePrefix + strings.TrimSpace(threadID)
}

func ParseDomainAgentRuntimeHandle(handle string) (string, bool) {
	threadID, ok := strings.CutPrefix(handle, domainAgentRuntimeHandlePrefix)
	return threadID, ok && strings.TrimSpace(threadID) == threadID && threadID != ""
}

// DomainAgentTurnHandle binds the exact accepted task attempt to one turn in
// the durable agent's provider thread.
func DomainAgentTurnHandle(turnID string) string {
	return domainAgentTurnHandlePrefix + strings.TrimSpace(turnID)
}

func ParseDomainAgentTurnHandle(handle string) (string, bool) {
	turnID, ok := strings.CutPrefix(handle, domainAgentTurnHandlePrefix)
	return turnID, ok && strings.TrimSpace(turnID) == turnID && turnID != ""
}
