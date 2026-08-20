package daemon

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
)

func TestM20FullDoctorIsReadOnlyAndEveryDiagnosticCapsAtTwentySamples(t *testing.T) {
	t.Run("live request appends no event", func(t *testing.T) {
		running := startTestServer(t, testConfig(t))
		client := localapi.NewClient(running.config.SocketPath)
		workspace, err := client.WorkspaceInit(context.Background(), "doctor-read-only", "doctor-read-only-workspace")
		if err != nil {
			t.Fatalf("WorkspaceInit() = %v", err)
		}
		before, err := client.EventsList(context.Background(), localapi.EventsListParams{
			Workspace: workspace.Workspace.ID,
			PageParams: localapi.PageParams{
				Limit: 1,
			},
		})
		if err != nil {
			t.Fatalf("EventsList(before full doctor) = %v", err)
		}

		result, err := client.SystemDoctorFull(context.Background())
		if err != nil || result.EventSequence != before.HighWater {
			t.Fatalf("SystemDoctorFull() = %#v, %v; want event %d", result, err, before.HighWater)
		}
		after, err := client.EventsList(context.Background(), localapi.EventsListParams{
			Workspace: workspace.Workspace.ID,
			PageParams: localapi.PageParams{
				Limit: 1,
			},
		})
		if err != nil || after.HighWater != before.HighWater {
			t.Fatalf("full doctor changed event high-water from %d to %d: %v", before.HighWater, after.HighWater, err)
		}
	})

	t.Run("stable registry counts and exact sample caps", func(t *testing.T) {
		queueSamples := make([]string, maximumDoctorSamples+7)
		artifactIssues := make([]recovery.ArtifactFilesystemIssue, maximumDoctorSamples+9)
		for index := range queueSamples {
			queueSamples[index] = fmt.Sprintf("queue-row-%02d-%s", index, strings.Repeat("x", 160))
		}
		for index := range artifactIssues {
			artifactIssues[index] = recovery.ArtifactFilesystemIssue{
				Code:   "unsafe_mode",
				Path:   fmt.Sprintf("artifacts/%02d/%s", index, strings.Repeat("p", 160)),
				Detail: strings.Repeat("unsafe artifact detail ", 150),
			}
		}
		integrity := store.CanonicalIntegrityReport{DurableQueues: []store.DurableQueueIntegrity{
			{Name: "run_job", Table: "run_jobs", RowCount: 13, Status: "failed", ViolationCount: 13, Samples: queueSamples[:13]},
			{Name: "check_job", Table: "check_jobs", RowCount: 14, Status: "failed", ViolationCount: 14, Samples: queueSamples[13:]},
		}}
		artifacts := recovery.ArtifactFilesystemReport{
			CheckedCount: int64(len(artifactIssues)),
			IssueCount:   int64(len(artifactIssues)),
			UnsafeCount:  int64(len(artifactIssues)),
			Issues:       artifactIssues,
		}
		checks := buildFullDoctorChecks(integrity, artifacts, databaseFileProbe{ByteSize: 1}, 0, domain.ManagedServiceExecutionHealth{}, 1, 0, 1, nil)
		gotCodes := make([]string, 0, len(checks))
		var artifactCheck, queueCheck *localapi.FullDoctorCheck
		for index := range checks {
			gotCodes = append(gotCodes, checks[index].Code)
			switch checks[index].Code {
			case "artifact_integrity":
				artifactCheck = &checks[index]
			case "durable_queues":
				queueCheck = &checks[index]
			}
		}
		if want := localapi.FullDoctorCheckOrder(); !reflect.DeepEqual(gotCodes, want) {
			t.Fatalf("full doctor codes = %v, want exact stable registry %v", gotCodes, want)
		}
		if artifactCheck == nil || artifactCheck.CheckedCount != int64(len(artifactIssues)) || artifactCheck.IssueCount != int64(len(artifactIssues)) || len(artifactCheck.Samples) != maximumDoctorSamples {
			t.Fatalf("artifact diagnostic = %#v; want exact counts and %d samples", artifactCheck, maximumDoctorSamples)
		}
		if queueCheck == nil || queueCheck.CheckedCount != 29 || queueCheck.IssueCount != 27 || len(queueCheck.Samples) != maximumDoctorSamples {
			t.Fatalf("queue diagnostic = %#v; want registry+row count 29, issue count 27, and %d samples", queueCheck, maximumDoctorSamples)
		}
		for _, check := range []*localapi.FullDoctorCheck{artifactCheck, queueCheck} {
			for _, sample := range check.Samples {
				if len(sample.EntityID) > 128 || len(sample.Code) > 64 || len(sample.Detail) > 2048 {
					t.Fatalf("doctor sample exceeded wire bounds: %#v", sample)
				}
			}
		}
	})
}
