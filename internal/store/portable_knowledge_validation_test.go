package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"crewfold/internal/domain"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

const (
	portableTestWorkspaceID = "ws_11111111111111111111111111111111"
	portableTestProjectID   = "prj_22222222222222222222222222222222"
	portableTestTaskID      = "task_33333333333333333333333333333333"
	portableTestRunID       = "run_44444444444444444444444444444444"
	portableTestItemA       = "know_55555555555555555555555555555555"
	portableTestItemB       = "know_66666666666666666666666666666666"
	portableTestRevisionA   = "krev_77777777777777777777777777777777"
	portableTestRevisionB   = "krev_88888888888888888888888888888888"
	portableTestSuccessor   = "krev_99999999999999999999999999999999"
	portableTestConflictID  = "kcon_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	portableTestTime0       = "2026-08-13T00:00:00Z"
	portableTestTime1       = "2026-08-13T00:00:01Z"
	portableTestTime2       = "2026-08-13T00:00:02Z"
	portableTestTime3       = "2026-08-13T00:00:03Z"
	portableTestTime4       = "2026-08-13T00:00:04Z"
)

func TestPortableRevisionValidatorAuthoritySourcesAndChronology(t *testing.T) {
	base := portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	validAgentProposal := base
	validAgentProposal.ProposedBy = portableTestRunID
	validAgentProposal.ProposedByType = domain.KnowledgeActorAgentRun
	if !validatePortableKnowledgeRevision(validAgentProposal) {
		t.Fatal("valid agent-run proposal descriptor was rejected")
	}
	offsetExpiration := base
	offsetExpiration.FreshnessPolicy, offsetExpiration.FreshUntil = domain.KnowledgeFreshExpiresAt, "2026-08-13T03:00:02+03:00"
	if !validatePortableKnowledgeRevision(offsetExpiration) {
		t.Fatal("valid RFC3339 offset fresh_until was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*domain.KnowledgeRevision)
	}{
		{name: "nonowner human proposer", mutate: func(revision *domain.KnowledgeRevision) { revision.ProposedBy = "alex" }},
		{name: "malformed agent proposer", mutate: func(revision *domain.KnowledgeRevision) {
			revision.ProposedBy, revision.ProposedByType = "run_bad", domain.KnowledgeActorAgentRun
		}},
		{name: "nonowner acceptor", mutate: func(revision *domain.KnowledgeRevision) { revision.AcceptedBy = "alex" }},
		{name: "agent acceptor", mutate: func(revision *domain.KnowledgeRevision) {
			revision.AcceptedBy, revision.AcceptedByType = portableTestRunID, domain.KnowledgeActorAgentRun
		}},
		{name: "unknown subsystem proposer", mutate: func(revision *domain.KnowledgeRevision) {
			revision.ProposedBy, revision.ProposedByType = "subsystem:other", domain.KnowledgeActorSubsystem
		}},
		{name: "title has surrounding whitespace", mutate: func(revision *domain.KnowledgeRevision) {
			revision.Title = " " + revision.Title
			revision.ContentHash = knowledgeContentHash(revision.Title, revision.Body)
		}},
		{name: "body has surrounding whitespace", mutate: func(revision *domain.KnowledgeRevision) {
			revision.Body += "\n"
			revision.ContentHash = knowledgeContentHash(revision.Title, revision.Body)
		}},
		{name: "decision note has surrounding whitespace", mutate: func(revision *domain.KnowledgeRevision) {
			revision.DecisionNote = " shipped "
		}},
		{name: "wrong task source prefix", mutate: func(revision *domain.KnowledgeRevision) {
			revision.Sources[0].ID = "meet_33333333333333333333333333333333"
		}},
		{name: "wrong meeting source prefix", mutate: func(revision *domain.KnowledgeRevision) {
			revision.Sources[0].Type, revision.Sources[0].ID = domain.KnowledgeSourceMeeting, portableTestTaskID
		}},
		{name: "wrong meeting proposal source prefix", mutate: func(revision *domain.KnowledgeRevision) {
			revision.Sources[0].Type, revision.Sources[0].ID = domain.KnowledgeSourceMeetingProposal, portableTestTaskID
		}},
		{name: "meeting proposal source is still proposed", mutate: func(revision *domain.KnowledgeRevision) {
			revision.Sources[0].Type, revision.Sources[0].ID = domain.KnowledgeSourceMeetingProposal, "proposal_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			revision.Sources[0].Revision = curatorSourceRevision - 1
		}},
		{name: "meeting proposal source has impossible later revision", mutate: func(revision *domain.KnowledgeRevision) {
			revision.Sources[0].Type, revision.Sources[0].ID = domain.KnowledgeSourceMeetingProposal, "proposal_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			revision.Sources[0].Revision = 100
		}},
		{name: "accepted before proposal", mutate: func(revision *domain.KnowledgeRevision) { revision.AcceptedAt = "2026-08-12T23:59:59Z" }},
		{name: "expires at proposal boundary", mutate: func(revision *domain.KnowledgeRevision) {
			revision.FreshnessPolicy, revision.FreshUntil = domain.KnowledgeFreshExpiresAt, revision.ProposedAt
		}},
		{name: "malformed expiration", mutate: func(revision *domain.KnowledgeRevision) {
			revision.FreshnessPolicy, revision.FreshUntil = domain.KnowledgeFreshExpiresAt, "later"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			revision := clonePortableRevision(t, base)
			test.mutate(&revision)
			if validatePortableKnowledgeRevision(revision) {
				t.Fatalf("mutated revision unexpectedly validated: %#v", revision)
			}
		})
	}

	rejected := portableRejectedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	rejected.RejectedBy = "alex"
	if validatePortableKnowledgeRevision(rejected) {
		t.Fatal("nonowner rejection actor unexpectedly validated")
	}
	rejected = portableRejectedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime1, portableTestTime0)
	if validatePortableKnowledgeRevision(rejected) {
		t.Fatal("rejection before proposal unexpectedly validated")
	}
	stale := portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime2)
	stale.StaleBy = "alex"
	if validatePortableKnowledgeRevision(stale) {
		t.Fatal("nonowner stale actor unexpectedly validated")
	}
	stale = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime2)
	stale.StaleReason = " obsolete "
	if validatePortableKnowledgeRevision(stale) {
		t.Fatal("stale reason with surrounding whitespace unexpectedly validated")
	}
	stale = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime2, portableTestTime1)
	if validatePortableKnowledgeRevision(stale) {
		t.Fatal("stale timestamp before acceptance unexpectedly validated")
	}
}

func TestPortableSnapshotValidatorOriginsCuratorAndReachableGraphs(t *testing.T) {
	base := portableSingleItemSnapshot(portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1))
	if err := validatePortableKnowledgeSnapshot(base); err != nil {
		t.Fatalf("valid baseline snapshot error = %v", err)
	}

	t.Run("item origin differs from first proposal", func(t *testing.T) {
		snapshot := clonePortableSnapshot(t, base)
		snapshot.Items[0].Item.CreatedAt = portableTestTime1
		assertInvalidPortableSnapshot(t, snapshot, "creation must equal")
	})
	t.Run("item creator differs from first proposal", func(t *testing.T) {
		snapshot := clonePortableSnapshot(t, base)
		snapshot.Items[0].Item.CreatedBy = "alex"
		assertInvalidPortableSnapshot(t, snapshot, "creation must equal")
	})
	t.Run("item creator type differs from first proposal", func(t *testing.T) {
		snapshot := clonePortableSnapshot(t, base)
		snapshot.Items[0].Item.CreatedByType = domain.KnowledgeActorAgentRun
		assertInvalidPortableSnapshot(t, snapshot, "creation must equal")
	})
	t.Run("unused task scope anchor", func(t *testing.T) {
		snapshot := clonePortableSnapshot(t, base)
		snapshot.TaskScopeAnchors = []domain.PortableKnowledgeTaskScopeAnchor{{
			TaskID: portableTestTaskID, WorkspaceID: portableTestWorkspaceID, ProjectID: portableTestProjectID,
			CreatedAt: portableTestTime0, CreatedBy: localOwnerActorID,
		}}
		snapshot.Counts.TaskScopeAnchors = 1
		assertInvalidPortableSnapshot(t, snapshot, "exactly equal")
	})

	curator := portableCuratorSnapshot(false)
	if err := validatePortableKnowledgeSnapshot(curator); err != nil {
		t.Fatalf("valid curator proposal error = %v", err)
	}
	acceptedCurator := portableCuratorSnapshot(true)
	if err := validatePortableKnowledgeSnapshot(acceptedCurator); err != nil {
		t.Fatalf("valid curator acceptance error = %v", err)
	}
	ownerAcceptedCurator := clonePortableSnapshot(t, acceptedCurator)
	ownerAcceptedRevision := &ownerAcceptedCurator.Items[0].Revisions[0]
	ownerAcceptedRevision.AcceptedBy, ownerAcceptedRevision.AcceptedByType = localOwnerActorID, domain.KnowledgeActorHuman
	ownerAcceptedRevision.DecisionNote = "owner reviewed exact curator copy"
	if err := validatePortableKnowledgeSnapshot(ownerAcceptedCurator); err != nil {
		t.Fatalf("valid owner acceptance of curator proposal error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*domain.PortableKnowledgeSnapshot)
		want   string
	}{
		{name: "curator task scoped", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Item.TaskScopeID = portableTestTaskID
			snapshot.Items[0].Revisions[0].TaskScopeID = portableTestTaskID
			snapshot.TaskScopeAnchors = []domain.PortableKnowledgeTaskScopeAnchor{{TaskID: portableTestTaskID, WorkspaceID: portableTestWorkspaceID, ProjectID: portableTestProjectID, CreatedAt: portableTestTime0, CreatedBy: localOwnerActorID}}
			snapshot.Counts.TaskScopeAnchors = 1
		}},
		{name: "curator finding", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Item.Type, snapshot.Items[0].Revisions[0].Type = domain.KnowledgeTypeFinding, domain.KnowledgeTypeFinding
		}},
		{name: "curator wrong quality", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0].Confidence = domain.KnowledgeConfidenceHigh
		}},
		{name: "curator task source", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0].Sources[0].Type = domain.KnowledgeSourceTask
			snapshot.Items[0].Revisions[0].Sources[0].ID = portableTestTaskID
		}},
		{name: "curator summary exceeds exact-copy bound", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			revision := &snapshot.Items[0].Revisions[0]
			revision.Body = strings.Repeat("x", maximumCuratorSummaryBytes+1)
			revision.ContentHash = knowledgeContentHash(revision.Title, revision.Body)
		}},
		{name: "curator source is not accepted proposal revision", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0].Sources[0].Revision = curatorSourceRevision - 1
		}, want: "knowledge revisions"},
		{name: "curator automatic acceptance note differs", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0].DecisionNote = "accepted by another curator rule"
		}},
		{name: "curator accepts owner proposal", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Item.CreatedBy, snapshot.Items[0].Item.CreatedByType = localOwnerActorID, domain.KnowledgeActorHuman
			snapshot.Items[0].Revisions[0].ProposedBy, snapshot.Items[0].Revisions[0].ProposedByType = localOwnerActorID, domain.KnowledgeActorHuman
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := clonePortableSnapshot(t, acceptedCurator)
			test.mutate(&snapshot)
			want := test.want
			if want == "" {
				want = "curator-described"
			}
			assertInvalidPortableSnapshot(t, snapshot, want)
		})
	}

	successor := portableSuccessorSnapshot()
	if err := validatePortableKnowledgeSnapshot(successor); err != nil {
		t.Fatalf("valid successor graph error = %v", err)
	}
	rejectedStar := portableRejectedSuccessorStarSnapshot()
	if err := validatePortableKnowledgeSnapshot(rejectedStar); err != nil {
		t.Fatalf("valid rejected-successor star error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*domain.PortableKnowledgeSnapshot)
	}{
		{name: "proposal times reverse revision order", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[2].ProposedAt = portableTestTime2
		}},
		{name: "later proposal predates prior rejection", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[2].ProposedAt = "2026-08-13T00:00:02.5Z"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := clonePortableSnapshot(t, rejectedStar)
			test.mutate(&snapshot)
			assertInvalidPortableSnapshot(t, snapshot, "creation order")
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*domain.PortableKnowledgeSnapshot)
		want   string
	}{
		{name: "successor proposed before predecessor acceptance", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[1].ProposedAt = portableTestTime0
		}, want: "leaves its item"},
		{name: "superseded without accepted successor", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions = snapshot.Items[0].Revisions[:1]
			snapshot.Counts.Revisions = 1
		}, want: "requires one accepted successor"},
		{name: "accepted successor with current predecessor", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			predecessor := &snapshot.Items[0].Revisions[0]
			predecessor.StateRevision, predecessor.CurrencyStatus = 2, domain.KnowledgeCurrencyCurrent
			successor := &snapshot.Items[0].Revisions[1]
			successor.StateRevision, successor.CurrencyStatus = 3, domain.KnowledgeCurrencyStale
			successor.StaleAt, successor.StaleBy, successor.StaleByType, successor.StaleReason = portableTestTime4, localOwnerActorID, domain.KnowledgeActorHuman, "expired"
		}, want: "accepted successor requires"},
		{name: "successor proposed after predecessor stale", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			predecessor := &snapshot.Items[0].Revisions[0]
			predecessor.CurrencyStatus, predecessor.StaleAt, predecessor.StaleBy, predecessor.StaleByType, predecessor.StaleReason = domain.KnowledgeCurrencyStale, "2026-08-13T00:00:01.5Z", localOwnerActorID, domain.KnowledgeActorHuman, "obsolete"
			successor := &snapshot.Items[0].Revisions[1]
			successor.StateRevision, successor.ReviewStatus, successor.CurrencyStatus = 1, domain.KnowledgeReviewProposed, domain.KnowledgeCurrencyPending
			successor.AcceptedAt, successor.AcceptedBy, successor.AcceptedByType = "", "", ""
		}, want: "leaves its item"},
		{name: "rejected successor with rejected predecessor", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0] = portableRejectedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
			snapshot.Items[0].Revisions[1] = portableRejectedRevision(portableTestSuccessor, portableTestItemA, 2, portableTestRevisionA, portableTestTime2, portableTestTime3)
		}, want: "leaves its item"},
		{name: "nonroot revision without predecessor", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[1].SupersedesRevisionID = ""
		}, want: "not contiguous"},
		{name: "successor names unknown predecessor", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[1].SupersedesRevisionID = portableTestRevisionB
		}, want: "leaves its item"},
		{name: "two accepted successors", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			third := portableAcceptedRevision("krev_cccccccccccccccccccccccccccccccc", portableTestItemA, 3, portableTestRevisionA, portableTestTime3, portableTestTime4)
			snapshot.Items[0].Revisions[1].StateRevision = 3
			snapshot.Items[0].Revisions[1].CurrencyStatus = domain.KnowledgeCurrencySuperseded
			snapshot.Items[0].Revisions = append(snapshot.Items[0].Revisions, third)
			snapshot.Counts.Revisions = 3
		}, want: "multiple live successors"},
		{name: "later rejected proposal targets superseded predecessor", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			third := portableRejectedRevision("krev_cccccccccccccccccccccccccccccccc", portableTestItemA, 3, portableTestRevisionA, portableTestTime4, "2026-08-13T00:00:05Z")
			snapshot.Items[0].Revisions = append(snapshot.Items[0].Revisions, third)
			snapshot.Counts.Revisions = 3
		}, want: "leaves its item"},
		{name: "later rejected proposal targets predecessor at equal transition time", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			third := portableRejectedRevision("krev_cccccccccccccccccccccccccccccccc", portableTestItemA, 3, portableTestRevisionA, portableTestTime3, portableTestTime4)
			snapshot.Items[0].Revisions = append(snapshot.Items[0].Revisions, third)
			snapshot.Counts.Revisions = 3
		}, want: "after its accepted successor"},
		{name: "later revision follows live proposal", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			proposed := &snapshot.Items[0].Revisions[1]
			proposed.StateRevision, proposed.ReviewStatus, proposed.CurrencyStatus = 1, domain.KnowledgeReviewProposed, domain.KnowledgeCurrencyPending
			proposed.AcceptedAt, proposed.AcceptedBy, proposed.AcceptedByType = "", "", ""
			third := portableRejectedRevision("krev_cccccccccccccccccccccccccccccccc", portableTestItemA, 3, portableTestRevisionA, portableTestTime2, portableTestTime3)
			snapshot.Items[0].Revisions = append(snapshot.Items[0].Revisions, third)
			snapshot.Counts.Revisions = 3
		}, want: "latest proposal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := clonePortableSnapshot(t, successor)
			test.mutate(&snapshot)
			assertInvalidPortableSnapshot(t, snapshot, test.want)
		})
	}
}

func TestPortableKnowledgeNativeCuratorRoundTrip(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	setup, proposal := createAcceptedCuratorMeeting(t, source)
	if proposal.Revision != curatorSourceRevision {
		t.Fatalf("accepted meeting proposal revision = %d, want %d", proposal.Revision, curatorSourceRevision)
	}
	if _, err := source.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "portable-curator-enable", CorrelationID: "portable-curator-enable-request",
	}); err != nil {
		t.Fatal(err)
	}
	processed, err := source.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, ApplySafe: true,
		IdempotencyKey: "portable-curator-process", CorrelationID: "portable-curator-process-request",
	})
	if err != nil || len(processed.Process.Derived) != 1 || len(processed.Process.Accepted) != 1 {
		t.Fatalf("ProcessCurator() = %#v, %v", processed, err)
	}

	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Manifest.Snapshot.Items) != 1 || len(exported.Manifest.Snapshot.Items[0].Revisions) != 1 {
		t.Fatalf("curator export snapshot = %#v", exported.Manifest.Snapshot)
	}
	revision := exported.Manifest.Snapshot.Items[0].Revisions[0]
	if revision.ProposedBy != domain.CuratorActorID || revision.AcceptedBy != domain.CuratorActorID ||
		revision.DecisionNote != curatorAutoAcceptanceNote || len(revision.Body) > maximumCuratorSummaryBytes ||
		len(revision.Sources) != 1 || revision.Sources[0].Revision != curatorSourceRevision {
		t.Fatalf("curator export revision = %#v", revision)
	}

	destination := openTestStore(t, t.TempDir(), Options{})
	if _, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
		ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown, ExpectedContentSHA256: exported.Manifest.ContentSHA256,
		CreateScope: true, Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-curator-import", CorrelationID: "portable-curator-import-request",
	}); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := destination.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
	})
	if err != nil || !bytes.Equal(roundTrip.ManifestJSON, exported.ManifestJSON) || !bytes.Equal(roundTrip.Markdown, exported.Markdown) {
		t.Fatalf("native curator round trip differs: %v", err)
	}
}

func TestPortableKnowledgeNativeSuccessorRoundTrip(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, source)
	task := createWorkTestTask(t, source, workspace.ID, project.ID, "portable successor source", "portable-successor-task")
	predecessor := proposeTaskKnowledge(t, source, workspace.ID, task.Task.ID, "portable-successor-predecessor", "Original contract", "The original contract is accepted", "")
	if _, err := source.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: predecessor.Revision.ID, ExpectedStateRevision: 1,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-successor-accept-predecessor", CorrelationID: "portable-successor-accept-predecessor-request",
	}); err != nil {
		t.Fatal(err)
	}
	successor := proposeTaskKnowledge(t, source, workspace.ID, task.Task.ID, "portable-successor-replacement", "Replacement contract", "The replacement contract is current", predecessor.Revision.ID)
	if _, err := source.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: successor.Revision.ID, ExpectedStateRevision: 1,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-successor-accept-replacement", CorrelationID: "portable-successor-accept-replacement-request",
	}); err != nil {
		t.Fatal(err)
	}

	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	destination := openTestStore(t, t.TempDir(), Options{})
	if _, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-successor-import", CorrelationID: "portable-successor-import-request",
	}); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := destination.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil || !bytes.Equal(roundTrip.ManifestJSON, exported.ManifestJSON) || !bytes.Equal(roundTrip.Markdown, exported.Markdown) {
		t.Fatalf("native successor round trip differs: %v", err)
	}
}

func TestPortableKnowledgeNativeProposedSuccessorWithLaterStalePredecessorRoundTrip(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, source)
	task := createWorkTestTask(t, source, workspace.ID, project.ID, "portable pending successor source", "portable-pending-successor-task")
	predecessor := proposeTaskKnowledge(t, source, workspace.ID, task.Task.ID, "portable-pending-predecessor", "Current contract", "The current contract can later become stale", "")
	accepted, err := source.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: predecessor.Revision.ID, ExpectedStateRevision: 1,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-pending-accept-predecessor", CorrelationID: "portable-pending-accept-predecessor-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposeTaskKnowledge(t, source, workspace.ID, task.Task.ID, "portable-pending-successor", "Pending replacement", "The replacement remains proposed", accepted.Revision.ID)
	if _, err := source.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: accepted.Revision.ID, ExpectedStateRevision: accepted.Revision.StateRevision,
		Reason: "source retired", Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-pending-stale-predecessor", CorrelationID: "portable-pending-stale-predecessor-request",
	}); err != nil {
		t.Fatal(err)
	}
	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	destination := openTestStore(t, t.TempDir(), Options{})
	if _, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-pending-import", CorrelationID: "portable-pending-import-request",
	}); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := destination.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil || !bytes.Equal(roundTrip.ManifestJSON, exported.ManifestJSON) || !bytes.Equal(roundTrip.Markdown, exported.Markdown) {
		t.Fatalf("pending successor/stale predecessor round trip differs: %v", err)
	}
}

func TestPortableKnowledgeNativeOffsetExpirationRoundTrip(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, source)
	task := createWorkTestTask(t, source, workspace.ID, project.ID, "portable offset expiration", "portable-offset-task")
	const freshUntil = "2126-08-13T03:00:00+03:00"
	proposed, err := source.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Type: domain.KnowledgeTypeFinding, Title: "Offset expiration",
		Body: "A valid RFC3339 offset is preserved exactly", Confidence: domain.KnowledgeConfidenceHigh,
		VerificationStatus: domain.KnowledgeVerificationSupported, FreshnessPolicy: domain.KnowledgeFreshExpiresAt,
		FreshUntil: freshUntil, Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: task.Task.ID, Role: domain.KnowledgeSourcePrimary}},
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-offset-propose", CorrelationID: "portable-offset-propose-request",
	})
	if err != nil || proposed.Revision.FreshUntil != freshUntil {
		t.Fatalf("offset proposal = %#v, %v", proposed, err)
	}
	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	destination := openTestStore(t, t.TempDir(), Options{})
	if _, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-offset-import", CorrelationID: "portable-offset-import-request",
	}); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := destination.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil || !bytes.Equal(roundTrip.ManifestJSON, exported.ManifestJSON) || roundTrip.Manifest.Snapshot.Items[0].Revisions[0].FreshUntil != freshUntil {
		t.Fatalf("offset expiration round trip = %#v, %v", roundTrip.Manifest.Snapshot, err)
	}
}

func TestPortableKnowledgeImportIdempotencyIsTargetScoped(t *testing.T) {
	destination := openTestStore(t, t.TempDir(), Options{})
	for _, scope := range []domain.PortableKnowledgeScope{
		{WorkspaceID: "ws_abababababababababababababababab", WorkspaceName: "portable-one", ProjectID: "prj_cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", ProjectName: "project-one"},
		{WorkspaceID: "ws_abababababababababababababababab", WorkspaceName: "portable-one", ProjectID: "prj_12121212121212121212121212121212", ProjectName: "project-two"},
	} {
		snapshot := domain.PortableKnowledgeSnapshot{Scope: scope, TaskScopeAnchors: []domain.PortableKnowledgeTaskScopeAnchor{}, Items: []domain.PortableKnowledgeItem{}, Contradictions: []domain.PortableKnowledgeContradiction{}}
		exported, err := renderPortableKnowledgeBundle(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		result, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
			WorkspaceIdentifier: scope.WorkspaceID, ProjectIdentifier: scope.ProjectID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
			ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
			IdempotencyKey: "portable-same-raw-key", CorrelationID: "portable-target-scoped-key-request",
		})
		if err != nil || result.Replayed {
			t.Fatalf("target-scoped import for %s = %#v, %v", scope.ProjectID, result, err)
		}
	}
}

func TestPortableKnowledgeImportedSourcesHaveNoLocalProvenanceAffinity(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, source)
	sourceTask := createWorkTestTask(t, source, workspace.ID, project.ID, "portable provenance source", "portable-provenance-source")
	proposed := proposeSearchKnowledge(t, source, workspace.ID, sourceTask.Task.ID, "", "portable-provenance-source-item", "portable-provenance", "imported source identity is descriptive", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationSupported)
	accepted := acceptSearchKnowledge(t, source, workspace.ID, proposed, "portable-provenance")
	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}

	destination := openTestStore(t, t.TempDir(), Options{})
	if _, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-provenance-import", CorrelationID: "portable-provenance-import-request",
	}); err != nil {
		t.Fatal(err)
	}
	// A later local task deliberately collides with the portable source ID and
	// metadata. Applicability is project-wide; this row must not turn descriptive
	// imported provenance into local task authority or affinity.
	if err := insertRawPortableTask(destination.db, sourceTask.Task.ID, workspace.ID, project.ID, sourceTask.Task.CreatedAt, sourceTask.Task.CreatedBy); err != nil {
		t.Fatal(err)
	}
	result, err := destination.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskIdentifier: sourceTask.Task.ID, Query: "portable-provenance",
	})
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Revision.ID != accepted.Revision.ID {
		t.Fatalf("imported provenance search = %#v, %v", result, err)
	}
	provenance := result.Matches[0].Explanation.Provenance
	if provenance.Rank != 4 || len(provenance.MatchedSourceIDs) != 0 {
		t.Fatalf("imported provenance affinity = %#v; want neutral rank with no matched local sources", provenance)
	}
}

func TestPortableKnowledgeImportedOriginLookupUsesEntityIndex(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	rows, err := storage.db.Query(`EXPLAIN QUERY PLAN SELECT 1 FROM knowledge_import_entities imported
WHERE imported.entity_type='knowledge_revision' AND imported.entity_id=?`, portableTestRevisionA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "knowledge_import_entities_entity_idx") {
		t.Fatalf("imported-origin lookup plan = %q; want entity index", plan)
	}
}

func TestPortableContradictionValidatorAuthorityParticipantsAndChronology(t *testing.T) {
	for _, status := range []string{
		domain.KnowledgeContradictionProposed,
		domain.KnowledgeContradictionOpen,
		"dismissed_direct",
		domain.KnowledgeContradictionDismissed,
		domain.KnowledgeContradictionResolved,
	} {
		t.Run("valid "+status, func(t *testing.T) {
			snapshot := portableContradictionSnapshot(status)
			if err := validatePortableKnowledgeSnapshot(snapshot); err != nil {
				t.Fatalf("valid %s contradiction error = %v", status, err)
			}
		})
	}
	agentReport := portableContradictionSnapshot(domain.KnowledgeContradictionProposed)
	agentReport.Contradictions[0].ReportedBy, agentReport.Contradictions[0].ReportedByType = portableTestRunID, domain.KnowledgeActorAgentRun
	if err := validatePortableKnowledgeSnapshot(agentReport); err != nil {
		t.Fatalf("valid agent-run report descriptor error = %v", err)
	}
	terminalAfterReport := portableContradictionSnapshot(domain.KnowledgeContradictionProposed)
	terminalAfterReport.Items[0].Revisions[0] = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime3)
	if err := validatePortableKnowledgeSnapshot(terminalAfterReport); err != nil {
		t.Fatalf("participant current at report but later stale error = %v", err)
	}
	directDismissAfterTerminal := portableContradictionSnapshot("dismissed_direct")
	directDismissAfterTerminal.Items[0].Revisions[0] = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime2)
	if err := validatePortableKnowledgeSnapshot(directDismissAfterTerminal); err != nil {
		t.Fatalf("direct dismissal after participant terminal error = %v", err)
	}

	tests := []struct {
		name   string
		base   string
		mutate func(*domain.PortableKnowledgeSnapshot)
	}{
		{name: "malformed agent reporter", base: domain.KnowledgeContradictionProposed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ReportedBy, snapshot.Contradictions[0].ReportedByType = "run_bad", domain.KnowledgeActorAgentRun
		}},
		{name: "nonowner human reporter", base: domain.KnowledgeContradictionProposed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ReportedBy = "alex"
		}},
		{name: "report note has surrounding whitespace", base: domain.KnowledgeContradictionProposed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ReportNote = " conflicting contracts "
		}},
		{name: "rejected participant", base: domain.KnowledgeContradictionProposed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0] = portableRejectedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
		}},
		{name: "reported before participant acceptance", base: domain.KnowledgeContradictionProposed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ReportedAt = portableTestTime0
		}},
		{name: "reported after participant stale", base: domain.KnowledgeContradictionProposed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0] = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime2)
			snapshot.Contradictions[0].ReportedAt = portableTestTime3
		}},
		{name: "reported after participant superseded", base: domain.KnowledgeContradictionProposed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			predecessor := &snapshot.Items[0].Revisions[0]
			predecessor.StateRevision, predecessor.CurrencyStatus = 3, domain.KnowledgeCurrencySuperseded
			successor := portableAcceptedRevision(portableTestSuccessor, portableTestItemA, 2, predecessor.ID, portableTestTime2, portableTestTime3)
			snapshot.Items[0].Revisions = append(snapshot.Items[0].Revisions, successor)
			snapshot.Counts.Revisions++
			snapshot.Contradictions[0].ReportedAt = portableTestTime4
		}},
		{name: "nonowner confirmer", base: domain.KnowledgeContradictionOpen, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ConfirmedBy = "alex"
		}},
		{name: "confirm note has surrounding whitespace", base: domain.KnowledgeContradictionOpen, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ConfirmNote = " confirmed "
		}},
		{name: "open contradiction has terminal participant", base: domain.KnowledgeContradictionOpen, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0] = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime4)
		}},
		{name: "confirmation before report", base: domain.KnowledgeContradictionOpen, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ConfirmedAt = portableTestTime1
		}},
		{name: "dismissal before confirmation", base: domain.KnowledgeContradictionDismissed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].DismissedAt = portableTestTime2
		}},
		{name: "participant ceased current before confirmation", base: domain.KnowledgeContradictionDismissed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0] = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime2)
		}},
		{name: "open participant ceased current before dismissal", base: domain.KnowledgeContradictionDismissed, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[0].Revisions[0] = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, "2026-08-13T00:00:03.5Z")
		}},
		{name: "nonowner dismissor", base: "dismissed_direct", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].DismissedBy = "alex"
		}},
		{name: "dismiss note has surrounding whitespace", base: "dismissed_direct", mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].DismissNote = " dismissed "
		}},
		{name: "resolution before confirmation", base: domain.KnowledgeContradictionResolved, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			contradiction := &snapshot.Contradictions[0]
			contradiction.ResolvedAt = portableTestTime2
			snapshot.Items[0].Revisions[0].StaleAt = portableTestTime2
		}},
		{name: "resolved participant ceased current before confirmation", base: domain.KnowledgeContradictionResolved, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[1].Revisions[0] = portableStaleRevision(portableTestRevisionB, portableTestItemB, 1, "", portableTestTime0, portableTestTime1, portableTestTime2)
		}},
		{name: "other participant ceased current before resolution", base: domain.KnowledgeContradictionResolved, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Items[1].Revisions[0] = portableStaleRevision(portableTestRevisionB, portableTestItemB, 1, "", portableTestTime0, portableTestTime1, "2026-08-13T00:00:03.5Z")
		}},
		{name: "nonowner resolver", base: domain.KnowledgeContradictionResolved, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ResolvedBy = "alex"
		}},
		{name: "resolution reason mismatches participant", base: domain.KnowledgeContradictionResolved, mutate: func(snapshot *domain.PortableKnowledgeSnapshot) {
			snapshot.Contradictions[0].ResolutionReason = ContradictionResolutionParticipantSuperseded
			snapshot.Contradictions[0].ResolutionNote = "knowledge revision " + portableTestRevisionA + " became superseded"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := portableContradictionSnapshot(test.base)
			test.mutate(&snapshot)
			assertInvalidPortableSnapshot(t, snapshot, "portable contradictions")
		})
	}
}

func TestPortableKnowledgeAnchorAndTaskIdentityDatabaseGuards(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	insert := func(taskID, createdAt, createdBy string) error {
		_, err := storage.db.Exec(`INSERT INTO knowledge_task_scope_anchors(task_id,workspace_id,project_id,created_at,created_by) VALUES(?,?,?,?,?)`,
			taskID, workspace.ID, project.ID, createdAt, createdBy)
		return err
	}
	for _, test := range []struct {
		name, taskID, createdAt, createdBy string
	}{
		{name: "noncanonical id", taskID: "task_NOT_HEX", createdAt: portableTestTime0, createdBy: localOwnerActorID},
		{name: "offset timestamp", taskID: "task_dddddddddddddddddddddddddddddddd", createdAt: "2026-08-13T03:00:00+03:00", createdBy: localOwnerActorID},
		{name: "nul creator", taskID: "task_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", createdAt: portableTestTime0, createdBy: "local\x00owner"},
		{name: "overlong creator", taskID: "task_abababababababababababababababab", createdAt: portableTestTime0, createdBy: strings.Repeat("a", 129)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := insert(test.taskID, test.createdAt, test.createdBy); err == nil {
				t.Fatal("invalid task-scope anchor unexpectedly persisted")
			}
		})
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_task_scope_anchors(task_id,workspace_id,project_id,created_at,created_by)
VALUES('task_ffffffffffffffffffffffffffffffff',?,?,?,CAST(X'80' AS TEXT))`, workspace.ID, project.ID, portableTestTime0); err == nil {
		t.Fatal("invalid UTF-8 task-scope anchor creator unexpectedly persisted")
	}
	const preexistingTaskID = "task_acacacacacacacacacacacacacacacac"
	if err := insertRawPortableTask(storage.db, preexistingTaskID, workspace.ID, project.ID, portableTestTime1, localOwnerActorID); err != nil {
		t.Fatal(err)
	}
	if err := insert(preexistingTaskID, portableTestTime0, localOwnerActorID); err == nil {
		t.Fatal("anchor differing from a preexisting task identity unexpectedly persisted")
	}
	if err := insert(portableTestTaskID, portableTestTime0, localOwnerActorID); err != nil {
		t.Fatal(err)
	}
	if err := insertRawPortableTask(storage.db, portableTestTaskID, workspace.ID, project.ID, portableTestTime1, localOwnerActorID); err == nil {
		t.Fatal("task with identity differing from its anchor unexpectedly persisted")
	}
	if err := insertRawPortableTask(storage.db, portableTestTaskID, workspace.ID, project.ID, portableTestTime0, localOwnerActorID); err != nil {
		t.Fatalf("task exactly matching its anchor error = %v", err)
	}
	for _, statement := range []string{
		`UPDATE tasks SET created_by='alex' WHERE id='` + portableTestTaskID + `'`,
		`UPDATE tasks SET created_at='` + portableTestTime1 + `' WHERE id='` + portableTestTaskID + `'`,
		`UPDATE tasks SET id='task_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id='` + portableTestTaskID + `'`,
		`UPDATE knowledge_task_scope_anchors SET created_by='alex' WHERE task_id='` + portableTestTaskID + `'`,
		`DELETE FROM knowledge_task_scope_anchors WHERE task_id='` + portableTestTaskID + `'`,
	} {
		if _, err := storage.db.Exec(statement); err == nil {
			t.Fatalf("anchored identity mutation unexpectedly succeeded: %s", statement)
		}
	}
}

func TestPortableKnowledgeImportScopeAndAnchorCollisions(t *testing.T) {
	source := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, source)
	task := createWorkTestTask(t, source, workspace.ID, project.ID, "portable anchor source", "portable-anchor-source")
	proposeSearchKnowledge(t, source, workspace.ID, task.Task.ID, task.Task.ID, "portable-anchor-proposal", "Anchored knowledge", "Exact anchor identity is portable", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationSupported)
	exported, err := source.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	anchor := exported.Manifest.Snapshot.TaskScopeAnchors[0]
	command := func(key string, create bool) ImportKnowledgeBundleCommand {
		return ImportKnowledgeBundleCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
			ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown, ExpectedContentSHA256: exported.Manifest.ContentSHA256,
			CreateScope: create, Actor: OwnerKnowledgeActor(), IdempotencyKey: key, CorrelationID: key + "-request"}
	}

	t.Run("missing anchor without create", func(t *testing.T) {
		destination := openTestStore(t, t.TempDir(), Options{})
		insertPortableTestScope(t, destination, exported.Manifest.Snapshot.Scope)
		assertPortableImportFailureWithoutWrites(t, destination, command("portable-missing-anchor", false), CodeKnowledgeImportScopeConflict)
	})
	t.Run("existing anchor metadata differs", func(t *testing.T) {
		destination := openTestStore(t, t.TempDir(), Options{})
		insertPortableTestScope(t, destination, exported.Manifest.Snapshot.Scope)
		if _, err := destination.db.Exec(`INSERT INTO knowledge_task_scope_anchors(task_id,workspace_id,project_id,created_at,created_by) VALUES(?,?,?,?,?)`,
			anchor.TaskID, anchor.WorkspaceID, anchor.ProjectID, portableTestTime0, anchor.CreatedBy); err != nil {
			t.Fatal(err)
		}
		assertPortableImportFailureWithoutWrites(t, destination, command("portable-anchor-metadata-conflict", true), CodeKnowledgeImportScopeConflict)
	})
	t.Run("target has extra anchor", func(t *testing.T) {
		destination := openTestStore(t, t.TempDir(), Options{})
		insertPortableTestScope(t, destination, exported.Manifest.Snapshot.Scope)
		for _, value := range []domain.PortableKnowledgeTaskScopeAnchor{
			anchor,
			{TaskID: "task_cccccccccccccccccccccccccccccccc", WorkspaceID: anchor.WorkspaceID, ProjectID: anchor.ProjectID, CreatedAt: portableTestTime0, CreatedBy: localOwnerActorID},
		} {
			if _, err := destination.db.Exec(`INSERT INTO knowledge_task_scope_anchors(task_id,workspace_id,project_id,created_at,created_by) VALUES(?,?,?,?,?)`,
				value.TaskID, value.WorkspaceID, value.ProjectID, value.CreatedAt, value.CreatedBy); err != nil {
				t.Fatal(err)
			}
		}
		assertPortableImportFailureWithoutWrites(t, destination, command("portable-extra-anchor", true), CodeKnowledgeImportScopeConflict)
	})
	t.Run("live task identity differs", func(t *testing.T) {
		destination := openTestStore(t, t.TempDir(), Options{})
		insertPortableTestScope(t, destination, exported.Manifest.Snapshot.Scope)
		if err := insertRawPortableTask(destination.db, anchor.TaskID, anchor.WorkspaceID, anchor.ProjectID, portableTestTime0, anchor.CreatedBy); err != nil {
			t.Fatal(err)
		}
		assertPortableImportFailureWithoutWrites(t, destination, command("portable-live-task-conflict", true), CodeKnowledgeImportScopeConflict)
	})
	t.Run("scope name differs", func(t *testing.T) {
		destination := openTestStore(t, t.TempDir(), Options{})
		scope := exported.Manifest.Snapshot.Scope
		scope.WorkspaceName += "-other"
		insertPortableTestScope(t, destination, scope)
		assertPortableImportFailureWithoutWrites(t, destination, command("portable-scope-name-conflict", false), CodeKnowledgeImportScopeConflict)
	})
	t.Run("preexisting exact anchor imports without create", func(t *testing.T) {
		destination := openTestStore(t, t.TempDir(), Options{})
		insertPortableTestScope(t, destination, exported.Manifest.Snapshot.Scope)
		if _, err := destination.db.Exec(`INSERT INTO knowledge_task_scope_anchors(task_id,workspace_id,project_id,created_at,created_by) VALUES(?,?,?,?,?)`,
			anchor.TaskID, anchor.WorkspaceID, anchor.ProjectID, anchor.CreatedAt, anchor.CreatedBy); err != nil {
			t.Fatal(err)
		}
		result, err := destination.ImportKnowledgeBundle(context.Background(), command("portable-exact-anchor", false))
		if err != nil || result.Created != (KnowledgeBundleImportCreated{}) || result.Replayed {
			t.Fatalf("exact-anchor import = %#v, %v", result, err)
		}
	})
}

func TestPortableKnowledgeImportAuditDatabaseLinkageAndCardinality(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	exported, err := storage.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("completion event linkage", func(t *testing.T) {
		withPortableRestoreTransaction(t, storage, func(ctx context.Context, tx *sql.Tx) {
			importID := "kimp_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			sequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_import", importID, 1,
				knowledgeImportedEvent, "wrong-completion-event", portableTestTime0, localOwnerActorID, domain.KnowledgeActorHuman,
				map[string]any{"bundle_id": exported.Manifest.BundleID, "project_id": project.ID, "content_sha256": exported.Manifest.ContentSHA256})
			if err != nil {
				t.Fatal(err)
			}
			if err := insertPortableTestReceipt(ctx, tx, importID, exported, exported.ManifestJSON, portableTestTime0, sequence); err == nil {
				t.Fatal("receipt linked to the wrong event type unexpectedly persisted")
			}
		})
	})
	t.Run("completion event count linkage", func(t *testing.T) {
		withPortableRestoreTransaction(t, storage, func(ctx context.Context, tx *sql.Tx) {
			importID := "kimp_bcbcbcbcbcbcbcbcbcbcbcbcbcbcbcbc"
			sequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_import", importID, 1,
				knowledgeImportCompletedEvent, "wrong-completion-count", portableTestTime0, localOwnerActorID, domain.KnowledgeActorHuman,
				map[string]any{"bundle_id": exported.Manifest.BundleID, "project_id": project.ID, "content_sha256": exported.Manifest.ContentSHA256,
					"item_count": 1, "revision_count": 0, "contradiction_count": 0})
			if err != nil {
				t.Fatal(err)
			}
			if err := insertPortableTestReceipt(ctx, tx, importID, exported, exported.ManifestJSON, portableTestTime0, sequence); err == nil {
				t.Fatal("receipt linked to completion event with different counts unexpectedly persisted")
			}
		})
	})

	for _, test := range []struct {
		name   string
		mutate func(*domain.PortableKnowledgeCounts)
	}{
		{name: "task scope anchors", mutate: func(counts *domain.PortableKnowledgeCounts) { counts.TaskScopeAnchors = 1 }},
		{name: "items", mutate: func(counts *domain.PortableKnowledgeCounts) { counts.Items = 1 }},
		{name: "revisions", mutate: func(counts *domain.PortableKnowledgeCounts) { counts.Revisions = 1 }},
		{name: "contradictions", mutate: func(counts *domain.PortableKnowledgeCounts) { counts.Contradictions = 1 }},
	} {
		t.Run("exact entity cardinality "+test.name, func(t *testing.T) {
			withPortableRestoreTransaction(t, storage, func(ctx context.Context, tx *sql.Tx) {
				manifest := exported.Manifest
				test.mutate(&manifest.Snapshot.Counts)
				manifestJSON, err := canonicalJSONLine(manifest)
				if err != nil {
					t.Fatal(err)
				}
				importID := "kimp_cccccccccccccccccccccccccccccccc"
				sequence := appendPortableTestCompletionEvent(t, ctx, tx, workspace.ID, project.ID, importID, manifest, portableTestTime0)
				if err := insertPortableTestReceipt(ctx, tx, importID, exported, manifestJSON, portableTestTime0, sequence); err == nil || !strings.Contains(err.Error(), "exact entity audit") {
					t.Fatalf("receipt without required %s entity audit error = %v", test.name, err)
				}
			})
		})
	}

	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "portable audit source", "portable-audit-source")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "portable-audit-left", "left", "left claim", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "portable-audit-right", "right", "right claim", "")
	reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
		ReportNote: "audit linkage", Actor: OwnerKnowledgeActor(), IdempotencyKey: "portable-audit-report", CorrelationID: "portable-audit-report-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor := domain.PortableKnowledgeTaskScopeAnchor{TaskID: task.Task.ID, WorkspaceID: workspace.ID, ProjectID: project.ID,
		CreatedAt: task.Task.CreatedAt, CreatedBy: task.Task.CreatedBy}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_task_scope_anchors(task_id,workspace_id,project_id,created_at,created_by) VALUES(?,?,?,?,?)`,
		anchor.TaskID, anchor.WorkspaceID, anchor.ProjectID, anchor.CreatedAt, anchor.CreatedBy); err != nil {
		t.Fatal(err)
	}

	t.Run("entity imported timestamp linkage", func(t *testing.T) {
		withPortableRestoreTransaction(t, storage, func(ctx context.Context, tx *sql.Tx) {
			importID := "kimp_cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?,?,?,NULL,?)`,
				importID, "task_scope_anchor", anchor.TaskID, portableTestTime1); err != nil {
				t.Fatal(err)
			}
			manifest := exported.Manifest
			manifest.Snapshot.Counts.TaskScopeAnchors = 1
			manifestJSON, err := canonicalJSONLine(manifest)
			if err != nil {
				t.Fatal(err)
			}
			completed := appendPortableTestCompletionEvent(t, ctx, tx, workspace.ID, project.ID, importID, manifest, portableTestTime0)
			if err := insertPortableTestReceipt(ctx, tx, importID, exported, manifestJSON, portableTestTime0, completed); err == nil || !strings.Contains(err.Error(), "exact entity audit") {
				t.Fatalf("receipt with mismatched entity imported_at error = %v", err)
			}
		})
	})

	t.Run("entity event linkage and seal", func(t *testing.T) {
		withPortableRestoreTransaction(t, storage, func(ctx context.Context, tx *sql.Tx) {
			importID := "kimp_dddddddddddddddddddddddddddddddd"
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?,?,?,NULL,?)`,
				importID, "task_scope_anchor", "task_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", portableTestTime0); err == nil {
				t.Fatal("nonexistent task-scope anchor audit unexpectedly persisted")
			}
			wrongSequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_revision", left.Revision.ID, left.Revision.StateRevision,
				knowledgeImportedEvent, "wrong-import-event", portableTestTime0, localOwnerActorID, domain.KnowledgeActorHuman,
				map[string]any{"bundle_import_id": "kimp_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "project_id": project.ID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?,?,?,?,?)`,
				importID, "knowledge_revision", left.Revision.ID, wrongSequence, portableTestTime0); err == nil {
				t.Fatal("revision audit linked to a different import unexpectedly persisted")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?,?,?,?,?)`,
				importID, "knowledge_item", left.Revision.ItemID, wrongSequence, portableTestTime0); err == nil {
				t.Fatal("knowledge item audit with an event unexpectedly persisted")
			}

			revisionSequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_revision", left.Revision.ID, left.Revision.StateRevision,
				knowledgeImportedEvent, "exact-import-event", portableTestTime0, localOwnerActorID, domain.KnowledgeActorHuman,
				map[string]any{"bundle_import_id": importID, "project_id": project.ID, "item_id": left.Revision.ItemID,
					"revision_number": left.Revision.RevisionNumber, "review_status": left.Revision.ReviewStatus, "currency_status": left.Revision.CurrencyStatus})
			if err != nil {
				t.Fatal(err)
			}
			contradiction := reported.Detail.Contradiction
			wrongContradictionSequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_contradiction", contradiction.ID, contradiction.StateRevision,
				contradictionImportedEvent, "wrong-contradiction-event", portableTestTime0, localOwnerActorID, domain.KnowledgeActorHuman,
				map[string]any{"bundle_import_id": importID, "project_id": project.ID, "status": domain.KnowledgeContradictionOpen})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?,?,?,?,?)`,
				importID, "knowledge_contradiction", contradiction.ID, wrongContradictionSequence, portableTestTime0); err == nil {
				t.Fatal("contradiction audit linked to a different status unexpectedly persisted")
			}
			contradictionSequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_contradiction", contradiction.ID, contradiction.StateRevision,
				contradictionImportedEvent, "exact-contradiction-event", portableTestTime0, localOwnerActorID, domain.KnowledgeActorHuman,
				map[string]any{"bundle_import_id": importID, "project_id": project.ID, "status": contradiction.Status})
			if err != nil {
				t.Fatal(err)
			}
			for _, entity := range []struct {
				typ, id  string
				sequence any
			}{
				{typ: "task_scope_anchor", id: anchor.TaskID},
				{typ: "knowledge_item", id: left.Revision.ItemID},
				{typ: "knowledge_revision", id: left.Revision.ID, sequence: revisionSequence},
				{typ: "knowledge_contradiction", id: contradiction.ID, sequence: contradictionSequence},
			} {
				if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?,?,?,?,?)`,
					importID, entity.typ, entity.id, entity.sequence, portableTestTime0); err != nil {
					t.Fatalf("insert exact %s audit: %v", entity.typ, err)
				}
			}
			manifest := exported.Manifest
			manifest.Snapshot.Counts = domain.PortableKnowledgeCounts{TaskScopeAnchors: 1, Items: 1, Revisions: 1, Contradictions: 1}
			manifestJSON, err := canonicalJSONLine(manifest)
			if err != nil {
				t.Fatal(err)
			}
			completed := appendPortableTestCompletionEvent(t, ctx, tx, workspace.ID, project.ID, importID, manifest, portableTestTime0)
			if err := insertPortableTestReceipt(ctx, tx, importID, KnowledgeBundleExportResult{Manifest: manifest, ManifestJSON: manifestJSON, Markdown: exported.Markdown}, manifestJSON, portableTestTime0, completed); err != nil {
				t.Fatalf("insert exactly linked receipt: %v", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_import_entities(import_id,entity_type,entity_id,event_sequence,imported_at) VALUES(?,?,?,?,?)`,
				importID, "knowledge_item", right.Revision.ItemID, nil, portableTestTime0); err == nil {
				t.Fatal("entity audit appended after receipt seal unexpectedly persisted")
			}
		})
	})
}

func TestPortableKnowledgeEncodingIsHardBounded(t *testing.T) {
	buffer := newPortableBoundedBuffer(8)
	if written, err := buffer.Write([]byte("12345678")); err != nil || written != 8 || len(buffer.Bytes()) != 8 {
		t.Fatalf("exact bounded write = %d/%v/%q", written, err, buffer.Bytes())
	}
	if _, err := buffer.Write([]byte("x")); !errors.Is(err, errPortableKnowledgeSizeLimit) || len(buffer.Bytes()) != 8 {
		t.Fatalf("overflow bounded write = %v/%q", err, buffer.Bytes())
	}
	if _, err := canonicalJSONWithLimit(map[string]string{"value": strings.Repeat("x", 64)}, 32, false); !errors.Is(err, errPortableKnowledgeSizeLimit) {
		t.Fatalf("bounded canonical JSON error = %v", err)
	}
	snapshot := portableSingleItemSnapshot(portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1))
	snapshot.Items[0].Revisions[0].Body = strings.Repeat("*", 128)
	if _, err := renderPortableKnowledgeMarkdownWithLimit(snapshot, 128); !errors.Is(err, errPortableKnowledgeSizeLimit) {
		t.Fatalf("bounded Markdown error = %v", err)
	}

	revision := portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	revision.Body = strings.Repeat("x", maximumKnowledgeBodyBytes)
	var total int64
	var limitErr error
	for limitErr == nil {
		limitErr = addPortableKnowledgePayloadBytes(&total, revision)
	}
	if ErrorCode(limitErr) != CodeInvalidKnowledgeBundle || total > maximumKnowledgeBundleFileBytes {
		t.Fatalf("payload lower-bound accounting = %d/%v", total, limitErr)
	}
}

func TestPortableKnowledgeManifestStructuralPreflightIsBounded(t *testing.T) {
	limits := portableKnowledgeStructuralLimits{
		TaskScopeAnchors: 2, Items: 2, Revisions: 2, Contradictions: 2, Sources: 2, JSONDepth: 3,
	}
	valid := `{"schema":"ignored","snapshot":{"task_scope_anchors":[{},{}],"items":[{"item":{},"revisions":[{"sources":[{},{}]}]},{"revisions":[{}]}],"contradictions":[{},{}]}}`
	if err := preflightPortableKnowledgeManifestStructure([]byte(valid), limits); err != nil {
		t.Fatalf("bounded structural preflight rejected valid shape: %v", err)
	}

	tests := []struct {
		name string
		json string
	}{
		{name: "too many anchors", json: `{"snapshot":{"task_scope_anchors":[{},{},{}]}}`},
		{name: "too many items", json: `{"snapshot":{"items":[{},{},{}]}}`},
		{name: "too many contradictions", json: `{"snapshot":{"contradictions":[{},{},{}]}}`},
		{name: "too many cumulative revisions", json: `{"snapshot":{"items":[{"revisions":[{}]},{"revisions":[{},{}]}]}}`},
		{name: "too many nested sources", json: `{"snapshot":{"items":[{"revisions":[{"sources":[{},{},{}]}]}]}}`},
		{name: "duplicate snapshot", json: `{"snapshot":{},"snapshot":{}}`},
		{name: "duplicate items", json: `{"snapshot":{"items":[],"items":[]}}`},
		{name: "duplicate revisions", json: `{"snapshot":{"items":[{"revisions":[],"revisions":[]}]}}`},
		{name: "duplicate sources", json: `{"snapshot":{"items":[{"revisions":[{"sources":[],"sources":[]}]}]}}`},
		{name: "unknown value exceeds nesting depth", json: `{"unknown":[[[[0]]]],"snapshot":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := preflightPortableKnowledgeManifestStructure([]byte(test.json), limits)
			if ErrorCode(err) != CodeInvalidKnowledgeBundle || !strings.Contains(err.Error(), "structural") {
				t.Fatalf("structural preflight error = %v, code %q", err, ErrorCode(err))
			}
		})
	}
}

func TestPortableKnowledgeExportPreflightRejectsOversizeBeforeHydration(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	tx, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const rows = maximumPortableKnowledgeItems
	if _, err := tx.Exec(`WITH RECURSIVE numbers(value) AS (
VALUES(1) UNION ALL SELECT value + 1 FROM numbers WHERE value < ?
) INSERT INTO knowledge_items(id,workspace_id,project_id,task_scope_id,type,created_at,created_by,created_by_type)
SELECT 'know_' || printf('%032x',value), ?, ?, NULL, 'decision', ?, 'local-owner', 'human' FROM numbers`,
		rows, workspace.ID, project.ID, portableTestTime0); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", maximumKnowledgeBodyBytes)
	if _, err := tx.Exec(`WITH RECURSIVE numbers(value) AS (
VALUES(1) UNION ALL SELECT value + 1 FROM numbers WHERE value < ?
) INSERT INTO knowledge_revisions(
id,item_id,revision_number,state_revision,title,body,content_hash,review_status,currency_status,confidence,verification_status,
freshness_policy,fresh_until,supersedes_revision_id,proposed_at,proposed_by,proposed_by_type,
accepted_at,accepted_by,accepted_by_type,rejected_at,rejected_by,rejected_by_type,stale_at,stale_by,stale_by_type,decision_note,stale_reason
) SELECT 'krev_' || printf('%032x',value), 'know_' || printf('%032x',value), 1, 1, 'bulk', ?, ?,
'proposed','pending','high','supported','until_superseded',NULL,NULL,?,'local-owner','human',
NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL FROM numbers`,
		rows, body, strings.Repeat("0", 64), portableTestTime0); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`WITH RECURSIVE numbers(value) AS (
VALUES(1) UNION ALL SELECT value + 1 FROM numbers WHERE value < ?
) INSERT INTO knowledge_sources(revision_id,ordinal,source_type,source_id,source_revision,role)
SELECT 'krev_' || printf('%032x',value),0,'task',?,1,'primary' FROM numbers`, rows, portableTestTaskID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = storage.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if ErrorCode(err) != CodeInvalidKnowledgeBundle || !strings.Contains(err.Error(), "representation limit") {
		t.Fatalf("oversize preflight export error = %v, code %q", err, ErrorCode(err))
	}
}

func TestPortableKnowledgeMarkdownEscapesInvisibleControls(t *testing.T) {
	revision := portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	revision.Body = "safe\x1b\a\t\x7f\u0085\u202eend"
	revision.ContentHash = knowledgeContentHash(revision.Title, revision.Body)
	snapshot := portableSingleItemSnapshot(revision)
	exported, err := renderPortableKnowledgeBundle(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Manifest.Snapshot.Items[0].Revisions[0].Body != revision.Body {
		t.Fatal("manifest did not preserve exact canonical body controls")
	}
	markdown := string(exported.Markdown)
	for _, visible := range []string{`\u{1b}`, `\u{7}`, `\u{9}`, `\u{7f}`, `\u{85}`, `\u{202e}`} {
		if !strings.Contains(markdown, visible) {
			t.Fatalf("Markdown %q does not contain visible escape %q", markdown, visible)
		}
	}
	for _, raw := range []string{"\x1b", "\a", "\t", "\x7f", "\u0085", "\u202e"} {
		if strings.Contains(markdown, raw) {
			t.Fatalf("Markdown contains raw invisible control %q", raw)
		}
	}
}

func TestPortableKnowledgeCanonicalStreamingDigestAndEscapingBoundary(t *testing.T) {
	value := map[string]string{"control": "\x01", "html": "<>&", "unicode": "България"}
	canonical, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonicalJSONSHA256(value)
	if err != nil || digest != bytesSHA256(canonical) {
		t.Fatalf("streaming canonical digest = %q/%v; want %q", digest, err, bytesSHA256(canonical))
	}
	if exact, err := canonicalJSONWithLimit(value, len(canonical), false); err != nil || !bytes.Equal(exact, canonical) {
		t.Fatalf("exact JSON limit = %q/%v", exact, err)
	}
	if _, err := canonicalJSONWithLimit(value, len(canonical)-1, false); !errors.Is(err, errPortableKnowledgeSizeLimit) {
		t.Fatalf("one-byte-short JSON limit error = %v", err)
	}
}

func TestPortableKnowledgeExportRejectsStateOutsidePortableCanonicalContract(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "portable strict export source", "portable-strict-export-task")
	title, body := " padded title ", "body"
	if _, err := storage.db.Exec(`INSERT INTO knowledge_items(id,workspace_id,project_id,task_scope_id,type,created_at,created_by,created_by_type)
VALUES(?,?,?,NULL,'decision',?,'local-owner','human')`, portableTestItemA, workspace.ID, project.ID, portableTestTime0); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_revisions(
id,item_id,revision_number,state_revision,title,body,content_hash,review_status,currency_status,confidence,verification_status,
freshness_policy,fresh_until,supersedes_revision_id,proposed_at,proposed_by,proposed_by_type,
accepted_at,accepted_by,accepted_by_type,rejected_at,rejected_by,rejected_by_type,stale_at,stale_by,stale_by_type,decision_note,stale_reason
) VALUES(?,?,1,1,?,?,?,?,?,'high','supported','until_superseded',NULL,NULL,?,'local-owner','human',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
		portableTestRevisionA, portableTestItemA, title, body, knowledgeContentHash(title, body), domain.KnowledgeReviewProposed,
		domain.KnowledgeCurrencyPending, portableTestTime0); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_sources(revision_id,ordinal,source_type,source_id,source_revision,role) VALUES(?,0,'task',?,1,'primary')`,
		portableTestRevisionA, task.Task.ID); err != nil {
		t.Fatal(err)
	}
	_, err := storage.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if ErrorCode(err) != CodeInvalidKnowledgeBundle {
		t.Fatalf("strict portable export error = %v, code %q", err, ErrorCode(err))
	}
}

func TestPortableKnowledgeMigrationUpgradesPopulatedCanonicalSchema(t *testing.T) {
	dataDir := t.TempDir()
	legacy := openPortableKnowledgeSchemaVersionForTest(t, dataDir, 14)
	workspace, project := initializeWorkTestProject(t, legacy)
	task := createWorkTestTask(t, legacy, workspace.ID, project.ID, "portable upgrade source", "portable-upgrade-task")
	left := acceptedContradictionKnowledge(t, legacy, workspace.ID, task.Task.ID, "portable-upgrade-left", "upgrade left", "left claim", task.Task.ID)
	right := acceptedContradictionKnowledge(t, legacy, workspace.ID, task.Task.ID, "portable-upgrade-right", "upgrade right", "right claim", task.Task.ID)
	contradiction := openContradiction(t, legacy, workspace.ID, left.Revision.ID, right.Revision.ID, "portable-upgrade-open")
	// The current binary writes the forward-compatible binding alongside the
	// legacy task_scope_id. Remove the scaffolding so the file on disk is an
	// exact populated v14 schema before Open applies migration 015/backfills it.
	if _, err := legacy.db.Exec(`DROP TABLE knowledge_item_task_scopes; DROP TABLE knowledge_task_scope_anchors`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	storage := openTestStore(t, dataDir, Options{})
	var anchors, bindings, curatorRows int64
	if err := storage.db.QueryRow(`SELECT
(SELECT COUNT(*) FROM knowledge_task_scope_anchors WHERE task_id=?),
(SELECT COUNT(*) FROM knowledge_item_task_scopes WHERE task_id=?),
(SELECT COUNT(*) FROM curator_rules WHERE workspace_id=? AND revision=1 AND enabled=0)`,
		task.Task.ID, task.Task.ID, workspace.ID).Scan(&anchors, &bindings, &curatorRows); err != nil {
		t.Fatal(err)
	}
	if anchors != 1 || bindings != 2 || curatorRows != 1 {
		t.Fatalf("migration backfill anchors/bindings/curator = %d/%d/%d; want 1/2/1", anchors, bindings, curatorRows)
	}
	exported, err := storage.ExportKnowledgeBundle(context.Background(), ExportKnowledgeBundleQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if exported.Manifest.Snapshot.Counts.TaskScopeAnchors != 1 || exported.Manifest.Snapshot.Counts.Items != 2 ||
		exported.Manifest.Snapshot.Counts.Revisions != 2 || exported.Manifest.Snapshot.Counts.Contradictions != 1 ||
		exported.Manifest.Snapshot.Contradictions[0].ID != contradiction.ID {
		t.Fatalf("upgraded portable snapshot = %#v", exported.Manifest.Snapshot)
	}
	destination := openTestStore(t, t.TempDir(), Options{})
	if _, err := destination.ImportKnowledgeBundle(context.Background(), ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ManifestJSON: exported.ManifestJSON, Markdown: exported.Markdown,
		ExpectedContentSHA256: exported.Manifest.ContentSHA256, CreateScope: true, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "portable-upgrade-import", CorrelationID: "portable-upgrade-import-request",
	}); err != nil {
		t.Fatal(err)
	}
}

func portableContradictionSnapshot(status string) domain.PortableKnowledgeSnapshot {
	left := portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	right := portableAcceptedRevision(portableTestRevisionB, portableTestItemB, 1, "", portableTestTime0, portableTestTime1)
	leftSnapshot := portableSingleItemSnapshot(left)
	rightItem := domain.KnowledgeItem{ID: portableTestItemB, WorkspaceID: portableTestWorkspaceID, ProjectID: portableTestProjectID,
		Type: domain.KnowledgeTypeDecision, CreatedAt: right.ProposedAt, CreatedBy: right.ProposedBy, CreatedByType: right.ProposedByType}
	leftSnapshot.Items = append(leftSnapshot.Items, domain.PortableKnowledgeItem{Item: rightItem, Revisions: []domain.KnowledgeRevision{right}})
	leftSnapshot.Counts.Items, leftSnapshot.Counts.Revisions, leftSnapshot.Counts.Contradictions = 2, 2, 1
	contradiction := domain.PortableKnowledgeContradiction{
		ID: portableTestConflictID, WorkspaceID: portableTestWorkspaceID, ProjectID: portableTestProjectID,
		LeftRevisionID: portableTestRevisionA, RightRevisionID: portableTestRevisionB,
		Status: domain.KnowledgeContradictionProposed, StateRevision: 1, ReportNote: "conflicting contracts",
		ReportedAt: portableTestTime2, ReportedBy: localOwnerActorID, ReportedByType: domain.KnowledgeActorHuman,
	}
	switch status {
	case domain.KnowledgeContradictionOpen:
		contradiction.Status, contradiction.StateRevision = domain.KnowledgeContradictionOpen, 2
		contradiction.ConfirmedAt, contradiction.ConfirmedBy, contradiction.ConfirmedByType = portableTestTime3, localOwnerActorID, domain.KnowledgeActorHuman
	case "dismissed_direct":
		contradiction.Status, contradiction.StateRevision = domain.KnowledgeContradictionDismissed, 2
		contradiction.DismissedAt, contradiction.DismissedBy, contradiction.DismissedByType = portableTestTime3, localOwnerActorID, domain.KnowledgeActorHuman
	case domain.KnowledgeContradictionDismissed:
		contradiction.Status, contradiction.StateRevision = domain.KnowledgeContradictionDismissed, 3
		contradiction.ConfirmedAt, contradiction.ConfirmedBy, contradiction.ConfirmedByType = portableTestTime3, localOwnerActorID, domain.KnowledgeActorHuman
		contradiction.DismissedAt, contradiction.DismissedBy, contradiction.DismissedByType = portableTestTime4, localOwnerActorID, domain.KnowledgeActorHuman
	case domain.KnowledgeContradictionResolved:
		contradiction.Status, contradiction.StateRevision = domain.KnowledgeContradictionResolved, 3
		contradiction.ConfirmedAt, contradiction.ConfirmedBy, contradiction.ConfirmedByType = portableTestTime3, localOwnerActorID, domain.KnowledgeActorHuman
		contradiction.ResolutionReason = ContradictionResolutionParticipantStale
		contradiction.ResolvedAt, contradiction.ResolvedBy, contradiction.ResolvedByType = portableTestTime4, localOwnerActorID, domain.KnowledgeActorHuman
		contradiction.ResolutionNote = "knowledge revision " + portableTestRevisionA + " became stale"
		leftSnapshot.Items[0].Revisions[0] = portableStaleRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1, portableTestTime4)
	}
	leftSnapshot.Contradictions = []domain.PortableKnowledgeContradiction{contradiction}
	return leftSnapshot
}

func portableAcceptedRevision(id, itemID string, number int64, supersedes, proposedAt, acceptedAt string) domain.KnowledgeRevision {
	title, body := "Portable title "+id[len(id)-2:], "Portable body "+id[len(id)-2:]
	return domain.KnowledgeRevision{
		ID: id, ItemID: itemID, WorkspaceID: portableTestWorkspaceID, ProjectID: portableTestProjectID,
		Type: domain.KnowledgeTypeDecision, RevisionNumber: number, StateRevision: 2,
		Title: title, Body: body, ContentHash: knowledgeContentHash(title, body),
		ReviewStatus: domain.KnowledgeReviewAccepted, CurrencyStatus: domain.KnowledgeCurrencyCurrent,
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationSupported,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded, SupersedesRevisionID: supersedes,
		ProposedAt: proposedAt, ProposedBy: localOwnerActorID, ProposedByType: domain.KnowledgeActorHuman,
		AcceptedAt: acceptedAt, AcceptedBy: localOwnerActorID, AcceptedByType: domain.KnowledgeActorHuman,
		Sources: []domain.KnowledgeSource{{Type: domain.KnowledgeSourceTask, ID: portableTestTaskID, Revision: 1, Role: domain.KnowledgeSourcePrimary, Ordinal: 0}},
	}
}

func portableRejectedRevision(id, itemID string, number int64, supersedes, proposedAt, rejectedAt string) domain.KnowledgeRevision {
	revision := portableAcceptedRevision(id, itemID, number, supersedes, proposedAt, rejectedAt)
	revision.ReviewStatus, revision.CurrencyStatus = domain.KnowledgeReviewRejected, domain.KnowledgeCurrencyPending
	revision.AcceptedAt, revision.AcceptedBy, revision.AcceptedByType = "", "", ""
	revision.RejectedAt, revision.RejectedBy, revision.RejectedByType = rejectedAt, localOwnerActorID, domain.KnowledgeActorHuman
	return revision
}

func portableStaleRevision(id, itemID string, number int64, supersedes, proposedAt, acceptedAt, staleAt string) domain.KnowledgeRevision {
	revision := portableAcceptedRevision(id, itemID, number, supersedes, proposedAt, acceptedAt)
	revision.StateRevision, revision.CurrencyStatus = 3, domain.KnowledgeCurrencyStale
	revision.StaleAt, revision.StaleBy, revision.StaleByType, revision.StaleReason = staleAt, localOwnerActorID, domain.KnowledgeActorHuman, "obsolete"
	return revision
}

func portableSingleItemSnapshot(revision domain.KnowledgeRevision) domain.PortableKnowledgeSnapshot {
	item := domain.KnowledgeItem{ID: revision.ItemID, WorkspaceID: revision.WorkspaceID, ProjectID: revision.ProjectID,
		TaskScopeID: revision.TaskScopeID, Type: revision.Type, CreatedAt: revision.ProposedAt,
		CreatedBy: revision.ProposedBy, CreatedByType: revision.ProposedByType}
	return domain.PortableKnowledgeSnapshot{
		Scope:  domain.PortableKnowledgeScope{WorkspaceID: portableTestWorkspaceID, WorkspaceName: "personal", ProjectID: portableTestProjectID, ProjectName: "engine"},
		Counts: domain.PortableKnowledgeCounts{Items: 1, Revisions: 1}, TaskScopeAnchors: []domain.PortableKnowledgeTaskScopeAnchor{},
		Items:          []domain.PortableKnowledgeItem{{Item: item, Revisions: []domain.KnowledgeRevision{revision}}},
		Contradictions: []domain.PortableKnowledgeContradiction{},
	}
}

func portableCuratorSnapshot(accepted bool) domain.PortableKnowledgeSnapshot {
	revision := portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	revision.ProposedBy, revision.ProposedByType = "subsystem:curator", domain.KnowledgeActorSubsystem
	revision.Confidence, revision.VerificationStatus = domain.KnowledgeConfidenceMedium, domain.KnowledgeVerificationSupported
	revision.Sources = []domain.KnowledgeSource{{Type: domain.KnowledgeSourceMeetingProposal, ID: "proposal_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Revision: curatorSourceRevision, Role: domain.KnowledgeSourcePrimary, Ordinal: 0}}
	if accepted {
		revision.AcceptedBy, revision.AcceptedByType = domain.CuratorActorID, domain.KnowledgeActorSubsystem
		revision.DecisionNote = curatorAutoAcceptanceNote
	} else {
		revision.StateRevision, revision.ReviewStatus, revision.CurrencyStatus = 1, domain.KnowledgeReviewProposed, domain.KnowledgeCurrencyPending
		revision.AcceptedAt, revision.AcceptedBy, revision.AcceptedByType = "", "", ""
	}
	snapshot := portableSingleItemSnapshot(revision)
	snapshot.Items[0].Item.CreatedBy, snapshot.Items[0].Item.CreatedByType = domain.CuratorActorID, domain.KnowledgeActorSubsystem
	return snapshot
}

func portableSuccessorSnapshot() domain.PortableKnowledgeSnapshot {
	predecessor := portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	predecessor.StateRevision, predecessor.CurrencyStatus = 3, domain.KnowledgeCurrencySuperseded
	successor := portableAcceptedRevision(portableTestSuccessor, portableTestItemA, 2, predecessor.ID, portableTestTime2, portableTestTime3)
	snapshot := portableSingleItemSnapshot(predecessor)
	snapshot.Items[0].Revisions = append(snapshot.Items[0].Revisions, successor)
	snapshot.Counts.Revisions = 2
	return snapshot
}

func portableRejectedSuccessorStarSnapshot() domain.PortableKnowledgeSnapshot {
	predecessor := portableAcceptedRevision(portableTestRevisionA, portableTestItemA, 1, "", portableTestTime0, portableTestTime1)
	first := portableRejectedRevision(portableTestSuccessor, portableTestItemA, 2, predecessor.ID, portableTestTime2, portableTestTime3)
	second := portableRejectedRevision("krev_cccccccccccccccccccccccccccccccc", portableTestItemA, 3, predecessor.ID, portableTestTime3, portableTestTime4)
	snapshot := portableSingleItemSnapshot(predecessor)
	snapshot.Items[0].Revisions = append(snapshot.Items[0].Revisions, first, second)
	snapshot.Counts.Revisions = 3
	return snapshot
}

func clonePortableRevision(t *testing.T, revision domain.KnowledgeRevision) domain.KnowledgeRevision {
	t.Helper()
	data, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	var clone domain.KnowledgeRevision
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func clonePortableSnapshot(t *testing.T, snapshot domain.PortableKnowledgeSnapshot) domain.PortableKnowledgeSnapshot {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var clone domain.PortableKnowledgeSnapshot
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func assertInvalidPortableSnapshot(t *testing.T, snapshot domain.PortableKnowledgeSnapshot, message string) {
	t.Helper()
	err := validatePortableKnowledgeSnapshot(snapshot)
	if ErrorCode(err) != CodeInvalidKnowledgeBundle || !strings.Contains(err.Error(), message) {
		t.Fatalf("validatePortableKnowledgeSnapshot() error = %v, code %s; want invalid containing %q", err, ErrorCode(err), message)
	}
}

func insertRawPortableTask(db *sql.DB, taskID, workspaceID, projectID, createdAt, createdBy string) error {
	_, err := db.Exec(`INSERT INTO tasks(
id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,
budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by
) VALUES(?,?,?,NULL,'portable raw task',NULL,'ready',NULL,100,0,0,0,1,?,?,?,?)`,
		taskID, workspaceID, projectID, createdAt, createdAt, createdBy, createdBy)
	return err
}

func openPortableKnowledgeSchemaVersionForTest(t *testing.T, dataDir string, version int) *Store {
	t.Helper()
	path := filepath.Join(dataDir, databaseFilename)
	database, err := driver.Open(databaseDSN(path), func(connection *sqlite3.Conn) error {
		return registerSQLiteExtensions(connection)
	})
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if _, err := database.Exec(`PRAGMA application_id = ` + fmt.Sprint(sqliteApplicationID)); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL
) STRICT`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	for _, migration := range migrations[:version] {
		tx, err := database.BeginTx(context.Background(), nil)
		if err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		if _, err := tx.Exec(migration.sql); err != nil {
			_ = tx.Rollback()
			_ = database.Close()
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)`, migration.version, migration.name, portableTestTime0); err != nil {
			_ = tx.Rollback()
			_ = database.Close()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`PRAGMA user_version = ` + fmt.Sprint(migration.version)); err != nil {
			_ = tx.Rollback()
			_ = database.Close()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if version == 14 {
		// Current proposal/read code understands the v15 forward binding. These
		// two scaffolding tables let it produce ordinary v14 native rows; callers
		// drop them before exercising the actual embedded v15 migration.
		if _, err := database.Exec(`CREATE TABLE knowledge_task_scope_anchors(
task_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, project_id TEXT NOT NULL, created_at TEXT NOT NULL, created_by TEXT NOT NULL
) STRICT;
CREATE TABLE knowledge_item_task_scopes(item_id TEXT PRIMARY KEY, task_id TEXT NOT NULL) STRICT;`); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	return &Store{db: database, path: path, clock: time.Now, restoreActive: new(atomic.Bool)}
}

func insertPortableTestScope(t *testing.T, storage *Store, scope domain.PortableKnowledgeScope) {
	t.Helper()
	now := scopeCreationTimestamp()
	if _, err := storage.db.Exec(`INSERT INTO workspaces(id,name,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,1,?,?,?,?)`,
		scope.WorkspaceID, scope.WorkspaceName, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec(`INSERT INTO projects(id,workspace_id,name,revision,created_at,updated_at,created_by,updated_by) VALUES(?,?,?,1,?,?,?,?)`,
		scope.ProjectID, scope.WorkspaceID, scope.ProjectName, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		t.Fatal(err)
	}
}

type portableTrustCounts struct {
	workspaces, projects, anchors, items, revisions, contradictions, imports, importEntities, events int64
}

func portableTrustBoundaryCounts(t *testing.T, storage *Store) portableTrustCounts {
	t.Helper()
	var counts portableTrustCounts
	if err := storage.db.QueryRow(`SELECT
(SELECT COUNT(*) FROM workspaces),(SELECT COUNT(*) FROM projects),(SELECT COUNT(*) FROM knowledge_task_scope_anchors),
(SELECT COUNT(*) FROM knowledge_items),(SELECT COUNT(*) FROM knowledge_revisions),(SELECT COUNT(*) FROM knowledge_contradictions),
(SELECT COUNT(*) FROM knowledge_imports),(SELECT COUNT(*) FROM knowledge_import_entities),(SELECT COUNT(*) FROM events)`).Scan(
		&counts.workspaces, &counts.projects, &counts.anchors, &counts.items, &counts.revisions, &counts.contradictions,
		&counts.imports, &counts.importEntities, &counts.events); err != nil {
		t.Fatal(err)
	}
	return counts
}

func assertPortableImportFailureWithoutWrites(t *testing.T, storage *Store, command ImportKnowledgeBundleCommand, code string) {
	t.Helper()
	before := portableTrustBoundaryCounts(t, storage)
	_, err := storage.ImportKnowledgeBundle(context.Background(), command)
	if ErrorCode(err) != code {
		t.Fatalf("ImportKnowledgeBundle() error = %v, code %q; want %q", err, ErrorCode(err), code)
	}
	if after := portableTrustBoundaryCounts(t, storage); after != before {
		t.Fatalf("failed import wrote state: before=%#v after=%#v", before, after)
	}
}

func withPortableRestoreTransaction(t *testing.T, storage *Store, run func(context.Context, *sql.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := storage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	storage.restoreActive.Store(true)
	defer func() {
		storage.restoreActive.Store(false)
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			t.Fatal(err)
		}
	}()
	run(ctx, tx)
}

func appendPortableTestCompletionEvent(t *testing.T, ctx context.Context, tx *sql.Tx, workspaceID, projectID, importID string,
	manifest domain.PortableKnowledgeBundleManifest, importedAt string,
) int64 {
	t.Helper()
	sequence, err := appendEventForActor(ctx, tx, workspaceID, "knowledge_import", importID, 1,
		knowledgeImportCompletedEvent, "portable-test-completed", importedAt, localOwnerActorID, domain.KnowledgeActorHuman,
		map[string]any{"bundle_id": manifest.BundleID, "project_id": projectID, "content_sha256": manifest.ContentSHA256,
			"item_count": manifest.Snapshot.Counts.Items, "revision_count": manifest.Snapshot.Counts.Revisions,
			"contradiction_count": manifest.Snapshot.Counts.Contradictions})
	if err != nil {
		t.Fatal(err)
	}
	return sequence
}

func insertPortableTestReceipt(ctx context.Context, tx *sql.Tx, importID string, exported KnowledgeBundleExportResult,
	manifestJSON []byte, importedAt string, completedSequence int64,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO knowledge_imports(
id,bundle_id,workspace_id,project_id,content_sha256,rendering_sha256,manifest_json,markdown,
idempotency_key,request_hash,imported_at,imported_by,imported_by_type,created_workspace,created_project,
created_task_scope_anchors,completed_event_sequence) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		importID, exported.Manifest.BundleID, exported.Manifest.Snapshot.Scope.WorkspaceID, exported.Manifest.Snapshot.Scope.ProjectID,
		exported.Manifest.ContentSHA256, exported.Manifest.Rendering.SHA256, manifestJSON, exported.Markdown,
		"portable-direct-receipt", strings.Repeat("a", 64), importedAt, localOwnerActorID, domain.KnowledgeActorHuman, 0, 0, 0, completedSequence)
	return err
}
