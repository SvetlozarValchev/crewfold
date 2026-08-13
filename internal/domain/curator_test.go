package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCuratorDomainJSONUsesBoundedAuditShapes(t *testing.T) {
	t.Parallel()
	queue := CuratorQueue{
		Rule: CuratorRule{ID: "crule_00000000000000000000000000000001", WorkspaceID: "ws_00000000000000000000000000000001",
			Name: CuratorRuleAcceptedMeetingResolutionCopy, Revision: 1, Enabled: false,
			CreatedAt: "2026-08-13T00:00:00Z", CreatedBy: CuratorActorID},
		Entries: []CuratorQueueEntry{},
	}
	encoded, err := json.Marshal(queue)
	if err != nil {
		t.Fatalf("json.Marshal(queue) error = %v", err)
	}
	if !strings.Contains(string(encoded), `"rule":`) || !strings.Contains(string(encoded), `"entries":[]`) || strings.Contains(string(encoded), "next_cursor") {
		t.Fatalf("queue JSON = %s", encoded)
	}
	process := CuratorProcess{Derived: []CuratorDerivation{}, Accepted: []CuratorAutoAcceptance{}, Skipped: []CuratorSkip{}}
	encoded, err = json.Marshal(process)
	if err != nil {
		t.Fatalf("json.Marshal(process) error = %v", err)
	}
	for _, expected := range []string{`"derived":[]`, `"accepted":[]`, `"skipped":[]`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("process JSON %s lacks %s", encoded, expected)
		}
	}
}
