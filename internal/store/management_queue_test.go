package store

import (
	"context"
	"fmt"
	"testing"
)

func TestSupervisorWorkspaceEnumerationPagesPastOneThousand(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	ctx := context.Background()
	tx, err := storage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin supervisor workspace boundary fixture = %v", err)
	}
	now := storage.nowText()
	const total = 1001
	for index := 0; index < total; index++ {
		workspaceID := fmt.Sprintf("ws_%032x", index+1)
		name := fmt.Sprintf("supervisor-page-%04d", index+1)
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id,name,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,1,?,?,?,?)`,
			workspaceID, name, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert supervisor workspace %d = %v", index, err)
		}
		sequence, err := appendEvent(ctx, tx, workspaceID, "supervisor_policy", workspaceID, 2,
			"supervisor.policy_configured", fmt.Sprintf("workspace-page-%04d", index+1), now,
			map[string]any{"enabled": true, "auto_schedule": true})
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("append supervisor workspace policy event %d = %v", index, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO supervisor_policies(
workspace_id,revision,enabled,max_active_runs,max_starting_runs,default_project_concurrency,
default_provider_concurrency,project_concurrency_json,provider_concurrency_json,auto_schedule,
auto_retry_limit,retry_cooldown_seconds,event_sequence,created_at,updated_at,created_by,updated_by
) VALUES (?,2,1,8,2,4,4,'{}','{}',1,0,0,?,?,?,?,?)`,
			workspaceID, sequence, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			_ = tx.Rollback()
			t.Fatalf("enable supervisor workspace %d = %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit supervisor workspace boundary fixture = %v", err)
	}

	seen := make([]string, 0, total)
	after := ""
	for {
		page, err := storage.SupervisorWorkspaceIDs(ctx, after, 100)
		if err != nil {
			t.Fatalf("SupervisorWorkspaceIDs(after=%q) = %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 100 {
			t.Fatalf("workspace page length = %d, want at most 100", len(page))
		}
		for _, workspaceID := range page {
			if len(seen) != 0 && workspaceID <= seen[len(seen)-1] {
				t.Fatalf("workspace page order regressed: %q after %q", workspaceID, seen[len(seen)-1])
			}
			seen = append(seen, workspaceID)
		}
		after = page[len(page)-1]
	}
	if len(seen) != total {
		t.Fatalf("paged enabled workspaces = %d, want %d", len(seen), total)
	}
	if seen[1000] != fmt.Sprintf("ws_%032x", total) {
		t.Fatalf("last paged workspace = %q, want boundary workspace", seen[1000])
	}
}
