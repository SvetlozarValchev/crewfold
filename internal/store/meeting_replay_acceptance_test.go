package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestM19MeetingMutationReplayAfterClaimExpiryHasNoReconciliationSideEffect(t *testing.T) {
	type replayCall func() (MeetingMutationResult, error)
	type prepareReplay func(t *testing.T, storage *Store, setup meetingTestSetup) (MeetingMutationResult, replayCall, replayCall)

	tests := []struct {
		name    string
		prepare prepareReplay
	}{
		{
			name: "create",
			prepare: func(t *testing.T, storage *Store, setup meetingTestSetup) (MeetingMutationResult, replayCall, replayCall) {
				t.Helper()
				participants := make([]string, 0, len(setup.participants))
				for _, participant := range setup.participants {
					participants = append(participants, participant.ID)
				}
				command := CreateMeetingCommand{
					WorkspaceIdentifier: setup.workspace.ID,
					OverlapID:           setup.overlap.ID,
					ParticipantAgents:   participants,
					FacilitatorAgent:    setup.facilitator.ID,
					Policy:              domain.MeetingPolicyOwnerDecision,
					Timeout:             time.Hour,
					IdempotencyKey:      "meeting-create-elapsed-replay",
					CorrelationID:       "request-meeting-create-elapsed-replay",
				}
				first, err := storage.CreateMeeting(context.Background(), command)
				if err != nil {
					t.Fatalf("CreateMeeting(first) error = %v", err)
				}
				changed := command
				changed.Timeout += time.Second
				return first,
					func() (MeetingMutationResult, error) { return storage.CreateMeeting(context.Background(), command) },
					func() (MeetingMutationResult, error) { return storage.CreateMeeting(context.Background(), changed) }
			},
		},
		{
			name: "run",
			prepare: func(t *testing.T, storage *Store, setup meetingTestSetup) (MeetingMutationResult, replayCall, replayCall) {
				t.Helper()
				created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
				command := RunMeetingCommand{
					WorkspaceIdentifier: setup.workspace.ID,
					MeetingID:           created.Detail.Meeting.ID,
					ExpectedRevision:    created.Detail.Meeting.Revision,
					Fixture:             sequenceMeetingFixture(setup),
					IdempotencyKey:      "meeting-run-elapsed-replay",
					CorrelationID:       "request-meeting-run-elapsed-replay",
				}
				first, err := storage.RunMeeting(context.Background(), command)
				if err != nil {
					t.Fatalf("RunMeeting(first) error = %v", err)
				}
				changed := command
				changed.ExpectedRevision++
				return first,
					func() (MeetingMutationResult, error) { return storage.RunMeeting(context.Background(), command) },
					func() (MeetingMutationResult, error) { return storage.RunMeeting(context.Background(), changed) }
			},
		},
		{
			name: "accept",
			prepare: func(t *testing.T, storage *Store, setup meetingTestSetup) (MeetingMutationResult, replayCall, replayCall) {
				t.Helper()
				created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
				proposed, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
					WorkspaceIdentifier: setup.workspace.ID,
					MeetingID:           created.Detail.Meeting.ID,
					ExpectedRevision:    created.Detail.Meeting.Revision,
					Fixture:             sequenceMeetingFixture(setup),
					IdempotencyKey:      "meeting-accept-replay-proposal",
					CorrelationID:       "request-meeting-accept-replay-proposal",
				})
				if err != nil {
					t.Fatalf("RunMeeting(accept fixture) error = %v", err)
				}
				command := AcceptMeetingCommand{
					WorkspaceIdentifier: setup.workspace.ID,
					MeetingID:           created.Detail.Meeting.ID,
					ExpectedRevision:    proposed.Detail.Meeting.Revision,
					DecisionNote:        "owner accepts the frozen proposal",
					IdempotencyKey:      "meeting-accept-elapsed-replay",
					CorrelationID:       "request-meeting-accept-elapsed-replay",
				}
				first, err := storage.AcceptMeeting(context.Background(), command)
				if err != nil {
					t.Fatalf("AcceptMeeting(first) error = %v", err)
				}
				changed := command
				changed.DecisionNote = "changed owner decision"
				return first,
					func() (MeetingMutationResult, error) { return storage.AcceptMeeting(context.Background(), command) },
					func() (MeetingMutationResult, error) { return storage.AcceptMeeting(context.Background(), changed) }
			},
		},
		{
			name: "takeover",
			prepare: func(t *testing.T, storage *Store, setup meetingTestSetup) (MeetingMutationResult, replayCall, replayCall) {
				t.Helper()
				created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
				fixture := sequenceMeetingFixture(setup)
				command := TakeoverMeetingCommand{
					WorkspaceIdentifier: setup.workspace.ID,
					MeetingID:           created.Detail.Meeting.ID,
					ExpectedRevision:    created.Detail.Meeting.Revision,
					Proposal:            *fixture.Proposal,
					DecisionNote:        "owner takes over with the frozen resolution",
					IdempotencyKey:      "meeting-takeover-elapsed-replay",
					CorrelationID:       "request-meeting-takeover-elapsed-replay",
				}
				first, err := storage.TakeoverMeeting(context.Background(), command)
				if err != nil {
					t.Fatalf("TakeoverMeeting(first) error = %v", err)
				}
				changed := command
				changed.DecisionNote = "changed takeover decision"
				return first,
					func() (MeetingMutationResult, error) { return storage.TakeoverMeeting(context.Background(), command) },
					func() (MeetingMutationResult, error) { return storage.TakeoverMeeting(context.Background(), changed) }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed := time.Date(2035, 7, 8, 9, 10, 11, 0, time.UTC)
			storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return observed }})
			setup := initializeMeetingTest(t, storage, 2)
			first, replay, changedReplay := test.prepare(t, storage, setup)
			witness := m19AddElapsedMeetingReplayWitness(t, storage, setup, test.name)

			observed = observed.Add(2 * time.Second)
			beforeEvents := m19StoreEventCount(t, storage)
			got, err := replay()
			if err != nil || !reflect.DeepEqual(got, first) {
				t.Fatalf("%s exact replay = %#v, %v; want frozen %#v", test.name, got, err, first)
			}
			m19AssertClaimReplayDidNotReconcile(t, storage, witness.Claim.ID, domain.ClaimActive, witness.Claim.Revision, beforeEvents)

			if _, err := changedReplay(); ErrorCode(err) != CodeIdempotencyConflict {
				t.Fatalf("%s changed replay error = %v, code = %q", test.name, err, ErrorCode(err))
			}
			m19AssertClaimReplayDidNotReconcile(t, storage, witness.Claim.ID, domain.ClaimActive, witness.Claim.Revision, beforeEvents)
		})
	}
}

func m19AddElapsedMeetingReplayWitness(t *testing.T, storage *Store, setup meetingTestSetup, name string) ClaimMutationResult {
	t.Helper()
	checkout := claimTestCheckout(t, storage, setup.workspace.ID, setup.project.ID)
	task := createWorkTestTask(t, storage, setup.workspace.ID, setup.project.ID, "elapsed meeting replay witness "+name, "meeting-replay-witness-task-"+name)
	return addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: setup.workspace.ID,
		ProjectIdentifier:   setup.project.ID,
		TaskID:              task.Task.ID,
		CheckoutIdentifier:  checkout.ID,
		Kind:                domain.ClaimKindPath,
		Target:              "meeting/replay/witness/" + name + "/**",
		Mode:                domain.ClaimModeExclusive,
		ConflictPolicy:      domain.ClaimPolicyNotify,
		LeaseDuration:       time.Second,
		IdempotencyKey:      "meeting-replay-witness-claim-" + name,
		CorrelationID:       "request-meeting-replay-witness-claim-" + name,
	})
}
