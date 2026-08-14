package loadtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store"
)

const (
	personalWorkspaceCount     = 1
	personalProjectCount       = 10
	personalAgentCount         = 100
	personalObjectiveCount     = 10
	personalTaskCount          = 1000
	personalEventCount         = 100000
	personalNoisyEventCount    = 80000
	personalLaunchProfileCount = 100
	personalUnownedEventCount  = 101 // workspace.created plus 100 agent.created facts
	personalNoisyCommitments   = 200
	personalQuietCommitments   = personalProjectCount - 1
	personalCommitmentCount    = personalNoisyCommitments + personalQuietCommitments
	personalBriefingReads      = 20
	personalBriefingClaims     = 128
	personalBriefingBytes      = 64 * 1024
	personalStatusOperations   = 100
	personalMessageOperations  = 100
	personalControlOperations  = personalStatusOperations + personalMessageOperations
	personalControlEventDelta  = 1 + 3*personalMessageOperations

	personalMaximumDuration      = 5 * time.Minute
	personalMaximumRSS           = 512 * 1024 * 1024
	personalMaximumBytes         = 1024 * 1024 * 1024
	personalWarmStartupMax       = 2 * time.Second
	personalControlP99           = time.Second
	personalControlMax           = 2 * time.Second
	personalReconciliationMax    = 2 * time.Second
	personalRecoveryOperationMax = 60 * time.Second
	personalProjectBriefingP99   = 2 * time.Second
	personalProjectBriefingMax   = 5 * time.Second
	personalWorkspaceBriefingP99 = 5 * time.Second
	personalWorkspaceBriefingMax = 10 * time.Second
	personalScenarioSchema       = "urn:crewfold:schema:fixture:fake-run-scenario:v1"
)

type labelSet struct {
	rolePrefix    string
	purposePrefix string
}

var defaultLabels = labelSet{
	rolePrefix:    "root-authority-supervisor",
	purposePrefix: "unrestricted-owner-control",
}

type tempDirectoryFactory func() (string, error)

type measurements struct {
	mutations          []time.Duration
	eventPages         []time.Duration
	projectionReads    []time.Duration
	projectBriefings   []time.Duration
	workspaceBriefings []time.Duration
	warmStartup        []time.Duration
	statusOperations   []time.Duration
	messageOperations  []time.Duration
	controlOperations  []time.Duration
	reconciliation     []time.Duration
	doctor             []time.Duration
	backupCreate       []time.Duration
	backupVerify       []time.Duration
	backupRestore      []time.Duration
	generation         time.Duration
	verification       time.Duration
}

type projectFixture struct {
	project       domain.Project
	checkout      domain.Checkout
	objective     domain.Objective
	phaseTask     domain.Task
	briefingTasks [2]domain.Task
}

type fixtureState struct {
	workspace            domain.Workspace
	projects             []projectFixture
	phaseAgents          []domain.AgentDefinition
	entityKeys           map[string]string
	projectOwners        map[string]string
	commitments          map[string]domain.DeliverableCommitment
	quietSources         map[string]string
	providerRefusalAgent domain.AgentDefinition
	providerRefusalTask  domain.Task
	controlThreadID      string
	controlMessages      map[string]int
	capacity             capacityProof
	briefing             briefingProof
}

type capacityProof struct {
	peakUnresolved              int64
	peakStarting                int64
	peakProviderUnresolved      int64
	peakProjectUnresolved       int64
	startingRefusalEventDelta   int64
	providerRefusalEventDelta   int64
	unresolvedRefusalEventDelta int64
	providerBProgress           int64
	saturatedProviderA          int64
	saturatedProviderB          int64
	saturatedStarting           int64
	controlEventDelta           int64
	asyncWakeBlocked            int64
	reconciliationEventDelta    int64
	reconciliationSettled       int64
	settledUnresolved           int64
	settledStarting             int64
}

type briefingProof struct {
	projectClaims    int64
	projectOmitted   int64
	projectBytes     int64
	projectReused    int64
	workspaceClaims  int64
	workspaceOmitted int64
	workspaceBytes   int64
	workspaceReused  int64
	quietVisible     int64
}

// RunPersonal100 builds, independently verifies, measures, and removes one
// exact provider-free personal-100 fixture. It accepts no caller path or
// runtime endpoint by design.
func RunPersonal100(ctx context.Context) (Report, error) {
	return runPersonal100(ctx, defaultLabels, func() (string, error) {
		return os.MkdirTemp("", "crewfold-personal-100-")
	})
}

func runPersonal100(ctx context.Context, labels labelSet, makeTemp tempDirectoryFactory) (report Report, returnedErr error) {
	report = Report{
		Schema:      personalLoadSchema,
		Profile:     Personal100Profile,
		Status:      "failed",
		Environment: currentEnvironment(),
		Timings:     []Timing{},
		Assertions:  []Assertion{},
	}
	if err := validateLabels(labels); err != nil {
		return report, err
	}
	root, err := makeTemp()
	if err != nil {
		return report, fmt.Errorf("create private personal-100 directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(root); removeErr != nil && returnedErr == nil {
			report.Status = "failed"
			returnedErr = fmt.Errorf("remove private personal-100 directory: %w", removeErr)
		}
	}()
	if err := os.Chmod(root, 0o700); err != nil {
		return report, fmt.Errorf("make personal-100 directory private: %w", err)
	}

	metrics := measurements{
		mutations: make([]time.Duration, 0, personalEventCount), eventPages: make([]time.Duration, 0, 128),
		projectionReads: make([]time.Duration, 0, 64), projectBriefings: make([]time.Duration, 0, personalBriefingReads),
		workspaceBriefings: make([]time.Duration, 0, personalBriefingReads),
		statusOperations:   make([]time.Duration, 0, personalStatusOperations), messageOperations: make([]time.Duration, 0, personalMessageOperations),
		controlOperations: make([]time.Duration, 0, personalControlOperations),
	}
	clock := newPersonalClock(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	generationStarted := time.Now()
	storage, err := store.Open(ctx, root, store.Options{
		Clock:                  clock.Now,
		RuntimeNodeID:          "33333333333333333333333333333333",
		RuntimeNodeFingerprint: "4444444444444444444444444444444444444444444444444444444444444444",
	})
	if err != nil {
		return report, fmt.Errorf("open canonical personal-100 Store: %w", err)
	}
	health, err := measured(&metrics.projectionReads, func() (store.DatabaseHealth, error) {
		return storage.Health(ctx)
	})
	if err != nil {
		_ = storage.Close()
		return report, fmt.Errorf("read personal-100 SQLite runtime version: %w", err)
	}
	report.Environment.SQLiteVersion = health.SQLiteVersion
	closed := false
	defer func() {
		if !closed {
			_ = storage.Close()
		}
	}()

	fixture, err := buildPersonal100(ctx, storage, root, labels, &metrics, clock)
	metrics.generation = time.Since(generationStarted)
	if err != nil {
		return report, err
	}

	verificationStarted := time.Now()
	verification, err := verifyPersonal100(ctx, storage, labels, fixture, &metrics)
	if err != nil {
		metrics.verification = time.Since(verificationStarted)
		return report, err
	}
	canonical, err := measured(&metrics.projectionReads, func() (store.CanonicalIntegrityReport, error) {
		return storage.VerifyCanonical(ctx, store.CanonicalVerifyOptions{Full: true})
	})
	metrics.verification = time.Since(verificationStarted)
	if err != nil {
		return report, fmt.Errorf("verify canonical personal-100 Store: %w", err)
	}
	if !canonical.Complete || canonical.Status != "ok" || canonical.EventHighWater != personalEventCount {
		return report, fmt.Errorf("canonical personal-100 verification failed: status=%s complete=%t high_water=%d failures=%v semantic_families=%v", canonical.Status, canonical.Complete, canonical.EventHighWater, canonical.Failures, canonical.SemanticFamilies)
	}
	if canonical.Quiescence.EventHighWater != personalEventCount || !canonical.Quiescence.Quiescent {
		return report, fmt.Errorf("personal-100 canonical cut is not quiescent at event %d", personalEventCount)
	}

	databasePath := storage.Path()
	if err := storage.Close(); err != nil {
		return report, fmt.Errorf("close personal-100 Store: %w", err)
	}
	closed = true
	if err := measurePersonalRecovery(ctx, root, clock, canonical, &metrics); err != nil {
		return report, err
	}
	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		return report, fmt.Errorf("measure personal-100 database: %w", err)
	}
	artifactBytes, err := fixtureArtifactBytes(root)
	if err != nil {
		return report, err
	}

	report.Counts = verification.counts
	report.LogicalSHA256 = verification.logicalSHA256
	report.Timings = []Timing{
		timing("generation", []time.Duration{metrics.generation}),
		timing("verification", []time.Duration{metrics.verification}),
		timing("store_mutation", metrics.mutations),
		timing("event_page_read", metrics.eventPages),
		timing("projection_read", metrics.projectionReads),
		timing("project_briefing", metrics.projectBriefings),
		timing("workspace_briefing", metrics.workspaceBriefings),
		timing("warm_startup", metrics.warmStartup),
		timing("saturated_status", metrics.statusOperations),
		timing("saturated_message", metrics.messageOperations),
		timing("saturated_control", metrics.controlOperations),
		timing("lease_reconciliation", metrics.reconciliation),
		timing("doctor_full", metrics.doctor),
		timing("backup_create", metrics.backupCreate),
		timing("backup_verify", metrics.backupVerify),
		timing("backup_restore", metrics.backupRestore),
	}
	report.Resources = Resources{
		PeakRSSBytes:  peakRSSBytes(),
		DatabaseBytes: databaseInfo.Size(),
		ArtifactBytes: artifactBytes,
		Goroutines:    runtime.NumGoroutine(),
		OpenFDs:       openFDCount(),
	}
	report.Assertions = personalAssertions(report, verification.unownedEvents, verification.launchProfiles, verification.runs, fixture.capacity, fixture.briefing)
	failed := make([]string, 0)
	for _, assertion := range report.Assertions {
		if !assertion.Passed {
			failed = append(failed, assertion.Name)
		}
	}
	if len(failed) != 0 {
		return report, &FailedAssertionsError{Names: failed}
	}
	report.Status = "ok"
	return report, nil
}

func validateLabels(labels labelSet) error {
	if labels.rolePrefix == "" || labels.purposePrefix == "" || len(labels.rolePrefix) > 200 || len(labels.purposePrefix) > 200 {
		return errors.New("personal-100 descriptive label prefixes must contain 1 to 200 characters")
	}
	return nil
}

func buildPersonal100(ctx context.Context, storage *store.Store, root string, labels labelSet, metrics *measurements, clock *personalClock) (fixtureState, error) {
	state := fixtureState{
		projects:        make([]projectFixture, 0, personalProjectCount),
		phaseAgents:     make([]domain.AgentDefinition, personalProjectCount),
		entityKeys:      make(map[string]string, 1400),
		projectOwners:   make(map[string]string, 1300),
		commitments:     make(map[string]domain.DeliverableCommitment, personalCommitmentCount),
		quietSources:    make(map[string]string, personalQuietCommitments),
		controlMessages: make(map[string]int, personalMessageOperations),
	}
	workspaceResult, err := measured(&metrics.mutations, func() (store.WorkspaceInitResult, error) {
		return storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
			Name: "personal-100", IdempotencyKey: "load-workspace", CorrelationID: "load-workspace-request",
		})
	})
	if err != nil {
		return state, fmt.Errorf("create personal-100 workspace: %w", err)
	}
	state.workspace = workspaceResult.Workspace
	state.entityKeys[entityMapKey("workspace", state.workspace.ID)] = "workspace/personal-100"

	for index := 0; index < personalProjectCount; index++ {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		name := fmt.Sprintf("project-%02d", index)
		checkoutPath := filepath.Join(root, "checkouts", name)
		gitPath := filepath.Join(checkoutPath, ".git")
		if err := os.MkdirAll(gitPath, 0o700); err != nil {
			return state, fmt.Errorf("create owned synthetic checkout %s: %w", name, err)
		}
		observation := personalObservation(checkoutPath, gitPath, index)
		registered, err := measured(&metrics.mutations, func() (store.ProjectRegistrationResult, error) {
			return storage.RegisterProject(ctx, store.RegisterProjectCommand{
				WorkspaceIdentifier: state.workspace.ID,
				Name:                name,
				WriteMode:           domain.WriteModeExclusive,
				IdempotencyKey:      "load-" + name,
				CorrelationID:       "load-" + name + "-request",
				Observation:         observation,
			})
		})
		if err != nil {
			return state, fmt.Errorf("register personal-100 project %s: %w", name, err)
		}
		logicalProject := "project/" + name
		state.entityKeys[entityMapKey("project", registered.Project.ID)] = logicalProject
		state.entityKeys[entityMapKey("repository", registered.Repository.ID)] = logicalProject + "/repository"
		state.entityKeys[entityMapKey("checkout", registered.Checkout.ID)] = logicalProject + "/checkout"
		state.projectOwners[entityMapKey("project", registered.Project.ID)] = logicalProject
		state.projectOwners[entityMapKey("repository", registered.Repository.ID)] = logicalProject
		state.projectOwners[entityMapKey("checkout", registered.Checkout.ID)] = logicalProject
		state.projects = append(state.projects, projectFixture{project: registered.Project, checkout: registered.Checkout})
	}

	scenario := domain.FakeScenario{
		Schema: personalScenarioSchema,
		Name:   "personal-load",
		Steps:  []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "provider-free capacity phase complete", Handoff: "bounded capacity phase settled"}},
	}
	for index := 0; index < personalAgentCount; index++ {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		name := fmt.Sprintf("agent-%03d", index)
		projectIndex := index % personalProjectCount
		provider := personalProvider(projectIndex)
		agentResult, err := measured(&metrics.mutations, func() (store.MutationResult[domain.AgentDefinition], error) {
			return storage.CreateAgent(ctx, store.CreateAgentCommand{
				WorkspaceIdentifier: state.workspace.ID,
				Name:                name,
				Role:                labels.rolePrefix + "-" + name,
				Provider:            provider,
				Runtime:             "fixture",
				MaxConcurrency:      1,
				IdempotencyKey:      "load-" + name,
				CorrelationID:       "load-" + name + "-request",
			})
		})
		if err != nil {
			return state, fmt.Errorf("create personal-100 agent %s: %w", name, err)
		}
		agent := agentResult.Value
		state.entityKeys[entityMapKey("agent", agent.ID)] = "agent/" + name
		if index < personalProjectCount {
			state.phaseAgents[projectIndex] = agent
		}
		if index == personalProjectCount {
			state.providerRefusalAgent = agent
		}

		project := &state.projects[projectIndex]
		profileResult, err := measured(&metrics.mutations, func() (store.MutationResult[domain.LaunchProfile], error) {
			return storage.CreateLaunchProfile(ctx, store.CreateLaunchProfileCommand{
				WorkspaceIdentifier:    state.workspace.ID,
				ProjectIdentifier:      project.project.ID,
				AgentIdentifier:        agent.ID,
				ExpectedAgentRevision:  agent.Revision,
				Purpose:                labels.purposePrefix + "-" + name,
				Runtime:                agent.Runtime,
				Provider:               agent.Provider,
				CheckoutIdentifier:     project.checkout.ID,
				Scenario:               scenario,
				AssignmentLeaseSeconds: 300,
				CapabilityTTLSeconds:   300,
				IdempotencyKey:         "load-profile-" + name,
				CorrelationID:          "load-profile-" + name + "-request",
			})
		})
		if err != nil {
			return state, fmt.Errorf("create personal-100 launch profile %s: %w", name, err)
		}
		profile := profileResult.Value
		logicalProject := "project/" + project.project.Name
		state.entityKeys[entityMapKey("launch_profile", profile.ID)] = logicalProject + "/profile/" + name
		state.projectOwners[entityMapKey("launch_profile", profile.ID)] = logicalProject
	}

	for projectIndex := range state.projects {
		project := &state.projects[projectIndex]
		objectiveName := fmt.Sprintf("objective-%02d", projectIndex)
		objectiveResult, err := measured(&metrics.mutations, func() (store.MutationResult[domain.Objective], error) {
			return storage.CreateObjective(ctx, store.CreateObjectiveCommand{
				WorkspaceIdentifier: state.workspace.ID,
				ProjectIdentifier:   project.project.ID,
				Title:               objectiveName,
				Budget:              domain.Budget{},
				IdempotencyKey:      "load-" + objectiveName,
				CorrelationID:       "load-" + objectiveName + "-request",
			})
		})
		if err != nil {
			return state, fmt.Errorf("create personal-100 objective %s: %w", objectiveName, err)
		}
		project.objective = objectiveResult.Value
		logicalProject := "project/" + project.project.Name
		state.entityKeys[entityMapKey("objective", project.objective.ID)] = logicalProject + "/objective"
		state.projectOwners[entityMapKey("objective", project.objective.ID)] = logicalProject

		for taskIndex := 0; taskIndex < personalTaskCount/personalProjectCount; taskIndex++ {
			if err := ctx.Err(); err != nil {
				return state, err
			}
			taskName := fmt.Sprintf("task-%02d-%03d", projectIndex, taskIndex)
			taskResult, err := measured(&metrics.mutations, func() (store.TaskMutationResult, error) {
				return storage.CreateTask(ctx, store.CreateTaskCommand{
					WorkspaceIdentifier: state.workspace.ID,
					ProjectIdentifier:   project.project.ID,
					ObjectiveID:         project.objective.ID,
					Title:               taskName,
					Description:         "deterministic provider-free personal load task",
					Priority:            taskIndex % 11,
					Budget:              domain.Budget{},
					IdempotencyKey:      "load-" + taskName,
					CorrelationID:       "load-" + taskName + "-request",
				})
			})
			if err != nil {
				return state, fmt.Errorf("create personal-100 task %s: %w", taskName, err)
			}
			task := taskResult.Detail.Task
			if taskIndex == 0 {
				project.phaseTask = task
			} else if taskIndex <= len(project.briefingTasks) {
				project.briefingTasks[taskIndex-1] = task
			}
			if projectIndex == 0 && taskIndex == 3 {
				state.providerRefusalTask = task
			}
			state.entityKeys[entityMapKey("task", task.ID)] = logicalProject + "/task/" + taskName
			state.projectOwners[entityMapKey("task", task.ID)] = logicalProject
		}
	}
	capacity, err := exercisePersonalCapacity(ctx, storage, root, &state, scenario, metrics, clock)
	if err != nil {
		return state, err
	}
	state.capacity = capacity

	baseEvents, baseNoisy, _, err := countOwnedEvents(ctx, storage, state.entityKeys, state.projectOwners, "project/project-00", &metrics.eventPages)
	if err != nil {
		return state, fmt.Errorf("count personal-100 base events: %w", err)
	}
	if baseEvents > personalEventCount || baseNoisy > personalNoisyEventCount {
		return state, fmt.Errorf("personal-100 base exceeds event envelope: events=%d noisy=%d", baseEvents, baseNoisy)
	}
	noisyCheckpoints := personalNoisyEventCount - baseNoisy - personalNoisyCommitments
	remainingCheckpoints := personalEventCount - baseEvents - noisyCheckpoints - personalCommitmentCount
	if remainingCheckpoints < 0 {
		return state, errors.New("personal-100 event allocation is negative")
	}
	if noisyCheckpoints < 0 {
		return state, errors.New("personal-100 noisy-project event allocation is negative")
	}
	checkpointIndex := int64(0)
	for index := int64(0); index < noisyCheckpoints; index++ {
		if err := createLoadCheckpoint(ctx, storage, state.workspace.ID, state.projects[0].project.ID, checkpointIndex, &metrics.mutations); err != nil {
			return state, err
		}
		checkpointIndex++
	}
	for index := int64(0); index < remainingCheckpoints; index++ {
		projectIndex := 1 + int(index%int64(personalProjectCount-1))
		if err := createLoadCheckpoint(ctx, storage, state.workspace.ID, state.projects[projectIndex].project.ID, checkpointIndex, &metrics.mutations); err != nil {
			return state, err
		}
		checkpointIndex++
	}
	if err := createPersonalBriefingCommitments(ctx, storage, &state, metrics); err != nil {
		return state, err
	}
	briefing, err := measurePersonalBriefings(ctx, storage, state, metrics)
	if err != nil {
		return state, err
	}
	state.briefing = briefing
	return state, nil
}

func createPersonalBriefingCommitments(ctx context.Context, storage *store.Store, state *fixtureState, metrics *measurements) error {
	create := func(projectIndex int, task domain.Task, key, title, logicalSuffix string) error {
		result, err := measured(&metrics.mutations, func() (store.DeliverableCommitmentMutationResult, error) {
			return storage.CreateDeliverableCommitment(ctx, store.CreateDeliverableCommitmentCommand{
				WorkspaceIdentifier: state.workspace.ID,
				TaskID:              task.ID,
				Key:                 key,
				Title:               title,
				Description:         "provider-free personal-scale owner decision",
				AcceptanceCriteria:  []string{"the owner accepts or explicitly resolves this current delivery fact"},
				IdempotencyKey:      "load-commitment-" + key,
				CorrelationID:       "load-commitment-" + key + "-request",
			})
		})
		if err != nil {
			return fmt.Errorf("create personal-100 briefing commitment %s: %w", key, err)
		}
		commitment := result.Commitment
		logicalProject := "project/" + state.projects[projectIndex].project.Name
		logicalCommitment := logicalProject + "/commitment/" + logicalSuffix
		state.commitments[commitment.ID] = commitment
		state.entityKeys[entityMapKey("deliverable_commitment", commitment.ID)] = logicalCommitment
		state.projectOwners[entityMapKey("deliverable_commitment", commitment.ID)] = logicalProject
		return nil
	}

	for index := 0; index < personalNoisyCommitments; index++ {
		task := state.projects[0].briefingTasks[index/100]
		key := fmt.Sprintf("noisy-%03d", index)
		if err := create(0, task, key, fmt.Sprintf("Noisy urgent fact %03d", index), key); err != nil {
			return err
		}
	}
	for projectIndex := 1; projectIndex < personalProjectCount; projectIndex++ {
		key := fmt.Sprintf("quiet-%02d", projectIndex)
		if err := create(projectIndex, state.projects[projectIndex].briefingTasks[0], key,
			fmt.Sprintf("Quiet urgent fact %02d", projectIndex), key); err != nil {
			return err
		}
		for id, commitment := range state.commitments {
			if commitment.Key == key {
				state.quietSources[state.projects[projectIndex].project.ID] = id
				break
			}
		}
	}
	if len(state.commitments) != personalCommitmentCount || len(state.quietSources) != personalQuietCommitments {
		return fmt.Errorf("personal-100 briefing commitment topology=%d/%d, want %d/%d",
			len(state.commitments), len(state.quietSources), personalCommitmentCount, personalQuietCommitments)
	}
	return nil
}

func measurePersonalBriefings(ctx context.Context, storage *store.Store, state fixtureState, metrics *measurements) (briefingProof, error) {
	proof := briefingProof{}
	var projectFirst, workspaceFirst domain.ManagementBriefing
	for repetition := 0; repetition < personalBriefingReads; repetition++ {
		briefing, err := measured(&metrics.projectBriefings, func() (domain.ManagementBriefing, error) {
			return storage.ShowManagementBriefing(ctx, store.ShowManagementBriefingQuery{
				WorkspaceIdentifier: state.workspace.ID,
				ScopeType:           domain.OwnerCheckpointProject,
				ScopeIdentifier:     state.projects[0].project.ID,
			})
		})
		if err != nil {
			return proof, fmt.Errorf("read personal-100 noisy-project briefing %d: %w", repetition, err)
		}
		if repetition == 0 {
			projectFirst = briefing
			claims, omitted, _, inspectErr := inspectPersonalBriefing(briefing, state, personalNoisyCommitments, state.projects[0].project.ID)
			if inspectErr != nil {
				return proof, inspectErr
			}
			proof.projectClaims, proof.projectOmitted, proof.projectBytes = claims, omitted, int64(briefing.ByteSize)
		}
		if repetition > 0 && samePersonalBriefing(projectFirst, briefing) {
			proof.projectReused++
		}
	}
	for repetition := 0; repetition < personalBriefingReads; repetition++ {
		briefing, err := measured(&metrics.workspaceBriefings, func() (domain.ManagementBriefing, error) {
			return storage.ShowManagementBriefing(ctx, store.ShowManagementBriefingQuery{
				WorkspaceIdentifier: state.workspace.ID,
				ScopeType:           domain.OwnerCheckpointWorkspace,
				ScopeIdentifier:     state.workspace.ID,
			})
		})
		if err != nil {
			return proof, fmt.Errorf("read personal-100 workspace briefing %d: %w", repetition, err)
		}
		if repetition == 0 {
			workspaceFirst = briefing
			claims, omitted, sources, inspectErr := inspectPersonalBriefing(briefing, state, personalCommitmentCount, "")
			if inspectErr != nil {
				return proof, inspectErr
			}
			proof.workspaceClaims, proof.workspaceOmitted, proof.workspaceBytes = claims, omitted, int64(briefing.ByteSize)
			for _, sourceID := range state.quietSources {
				if sources[sourceID] {
					proof.quietVisible++
				}
			}
		}
		if repetition > 0 && samePersonalBriefing(workspaceFirst, briefing) {
			proof.workspaceReused++
		}
	}
	return proof, nil
}

func inspectPersonalBriefing(briefing domain.ManagementBriefing, state fixtureState, expectedSources int, exactProjectID string) (claims, omitted int64, sources map[string]bool, returnedErr error) {
	contentHash, contentHashErr := hex.DecodeString(briefing.ContentSHA256)
	if briefing.ID == "" || briefing.Revision != 1 || briefing.EvaluatedAt == "" || briefing.CheckpointID != "" ||
		contentHashErr != nil || len(contentHash) != sha256.Size ||
		!briefing.CaughtUp || briefing.EventCursor != personalEventCount || briefing.CutoffEventSequence != personalEventCount ||
		briefing.UnknownEventType != "" || briefing.UnknownEventSequence != 0 || briefing.SinceEventSequence != 0 ||
		len(briefing.Claims) < 1 || len(briefing.Claims) > personalBriefingClaims || briefing.ByteSize < 1 || briefing.ByteSize > personalBriefingBytes {
		return 0, 0, nil, fmt.Errorf("personal-100 briefing envelope differs: scope=%#v claims=%d bytes=%d cursor=%d/%d",
			briefing.Scope, len(briefing.Claims), briefing.ByteSize, briefing.EventCursor, briefing.CutoffEventSequence)
	}
	if exactProjectID == "" {
		if briefing.Scope.Type != domain.OwnerCheckpointWorkspace || briefing.Scope.WorkspaceID != state.workspace.ID ||
			briefing.Scope.ProjectID != "" || briefing.Scope.ObjectiveID != "" || briefing.Scope.TaskID != "" {
			return 0, 0, nil, fmt.Errorf("personal-100 workspace briefing scope differs: %#v", briefing.Scope)
		}
	} else if briefing.Scope.Type != domain.OwnerCheckpointProject || briefing.Scope.WorkspaceID != state.workspace.ID ||
		briefing.Scope.ProjectID != exactProjectID || briefing.Scope.ObjectiveID != "" || briefing.Scope.TaskID != "" {
		return 0, 0, nil, fmt.Errorf("personal-100 project briefing scope differs: %#v", briefing.Scope)
	}
	sources = make(map[string]bool, len(briefing.Claims))
	for _, claim := range briefing.Claims {
		if claim.Kind != domain.BriefingClaimUnmetCommitment || claim.Urgency != domain.OutcomeAttentionNow ||
			claim.Status != domain.BriefingClaimStatusUnmet || len(claim.Sources) != 1 {
			return 0, 0, nil, fmt.Errorf("personal-100 briefing claim differs: %#v", claim)
		}
		source := claim.Sources[0]
		commitment, exists := state.commitments[source.EntityID]
		if !exists || source.EntityType != "deliverable_commitment" || source.Revision != 1 ||
			source.ContentSHA256 != commitment.ContentSHA256 || source.EventSequence < 1 ||
			claim.ProjectID != commitment.ProjectID || claim.SourceEventSequence != source.EventSequence ||
			(exactProjectID != "" && commitment.ProjectID != exactProjectID) || sources[source.EntityID] {
			return 0, 0, nil, fmt.Errorf("personal-100 briefing source differs: %#v", source)
		}
		sources[source.EntityID] = true
	}
	for _, omission := range briefing.Omitted {
		if omission.Count < 1 || omission.Section != domain.BriefingSectionDeviationsUnmet ||
			(omission.Reason != domain.BriefingOmittedClaimLimit && omission.Reason != domain.BriefingOmittedByteLimit) {
			return 0, 0, nil, fmt.Errorf("personal-100 briefing omission differs: %#v", omission)
		}
		omitted += int64(omission.Count)
	}
	claims = int64(len(briefing.Claims))
	if claims+omitted != int64(expectedSources) {
		return 0, 0, nil, fmt.Errorf("personal-100 briefing accounting=%d selected + %d omitted, want %d", claims, omitted, expectedSources)
	}
	return claims, omitted, sources, nil
}

func samePersonalBriefing(first, current domain.ManagementBriefing) bool {
	return first.ID != "" && current.ID == first.ID && current.Revision == first.Revision &&
		current.ContentSHA256 == first.ContentSHA256 && current.EvaluatedAt == first.EvaluatedAt &&
		current.EventCursor == first.EventCursor && current.ByteSize == first.ByteSize
}

func personalProvider(projectIndex int) string {
	switch {
	case projectIndex < 4:
		return "fixture-a"
	case projectIndex < 8:
		return "fixture-b"
	default:
		return "fixture-c"
	}
}

func exercisePersonalCapacity(ctx context.Context, storage *store.Store, root string, state *fixtureState, scenario domain.FakeScenario, metrics *measurements, clock *personalClock) (capacityProof, error) {
	proof := capacityProof{}
	assigned := make([]domain.Task, personalProjectCount)
	activeRuns := make([]domain.Run, 0, 8)
	assignTask := func(task domain.Task, agent domain.AgentDefinition, key string, leaseSeconds int64) (domain.Task, error) {
		result, err := measured(&metrics.mutations, func() (store.TaskMutationResult, error) {
			return storage.AssignTask(ctx, store.AssignTaskCommand{
				WorkspaceIdentifier: state.workspace.ID, TaskID: task.ID, AgentIdentifier: agent.ID,
				LeaseSeconds: leaseSeconds, ExpectedRevision: task.Revision,
				IdempotencyKey: key, CorrelationID: key + "-request",
			})
		})
		if err != nil {
			return domain.Task{}, err
		}
		return result.Detail.Task, nil
	}
	assign := func(projectIndex int) error {
		if assigned[projectIndex].ID != "" {
			return nil
		}
		result, err := assignTask(state.projects[projectIndex].phaseTask, state.phaseAgents[projectIndex], fmt.Sprintf("load-capacity-assign-%02d", projectIndex), 3600)
		if err != nil {
			return fmt.Errorf("assign personal-100 capacity task %d: %w", projectIndex, err)
		}
		assigned[projectIndex] = result
		return nil
	}
	createTaskRun := func(task domain.Task, agent domain.AgentDefinition, checkout domain.Checkout, key string) (store.RunMutationResult, error) {
		return measured(&metrics.mutations, func() (store.RunMutationResult, error) {
			return storage.CreateRun(ctx, store.CreateRunCommand{
				WorkspaceIdentifier: state.workspace.ID, TaskID: task.ID, CheckoutIdentifier: checkout.ID,
				Runtime: agent.Runtime, Provider: agent.Provider, Scenario: scenario,
				ExpectedTaskRevision: task.Revision, CapabilityTTL: time.Hour,
				IdempotencyKey: key, CorrelationID: key + "-request",
			})
		})
	}
	create := func(projectIndex int) (store.RunMutationResult, error) {
		return createTaskRun(assigned[projectIndex], state.phaseAgents[projectIndex], state.projects[projectIndex].checkout, fmt.Sprintf("load-capacity-run-%02d", projectIndex))
	}
	activate := func(projectIndex int, created store.RunMutationResult) error {
		starting, err := measured(&metrics.mutations, func() (domain.Run, error) {
			return storage.MarkRunStarting(ctx, created.Detail.Run.ID, fmt.Sprintf("load-capacity-run-%02d-starting", projectIndex))
		})
		if err != nil {
			return fmt.Errorf("mark personal-100 capacity run %d starting: %w", projectIndex, err)
		}
		started, err := measured(&metrics.mutations, func() (domain.RunDetail, error) {
			return storage.MarkRunStarted(ctx, starting.ID, fmt.Sprintf("load-runtime-%02d", projectIndex), fmt.Sprintf("load-provider-%02d", projectIndex), fmt.Sprintf("load-capacity-run-%02d-started", projectIndex))
		})
		if err != nil {
			return fmt.Errorf("mark personal-100 capacity run %d active: %w", projectIndex, err)
		}
		activeRuns = append(activeRuns, started.Run)
		logicalProject := "project/" + state.projects[projectIndex].project.Name
		logicalTask := state.entityKeys[entityMapKey("task", assigned[projectIndex].ID)]
		state.entityKeys[entityMapKey("run", started.Run.ID)] = logicalTask + "/run"
		state.entityKeys[entityMapKey("context_packet", started.Run.ContextPacketID)] = logicalTask + "/context"
		state.projectOwners[entityMapKey("run", started.Run.ID)] = logicalProject
		state.projectOwners[entityMapKey("context_packet", started.Run.ContextPacketID)] = logicalProject
		return nil
	}
	observeHealth := func() (domain.ExecutionHealth, error) {
		health, err := measured(&metrics.projectionReads, func() (domain.ExecutionHealth, error) { return storage.ExecutionHealth(ctx) })
		if err != nil {
			return domain.ExecutionHealth{}, err
		}
		proof.peakUnresolved = maxInt64(proof.peakUnresolved, health.Node.Unresolved)
		proof.peakStarting = maxInt64(proof.peakStarting, health.Node.Starting)
		for _, provider := range health.Providers {
			proof.peakProviderUnresolved = maxInt64(proof.peakProviderUnresolved, provider.Unresolved)
		}
		for _, project := range health.Projects {
			proof.peakProjectUnresolved = maxInt64(proof.peakProjectUnresolved, project.Unresolved)
		}
		return health, nil
	}
	refusal := func(createAttempt func() (store.RunMutationResult, error), dimension, scope string, expectedActual, expectedLimit int) (int64, error) {
		before, err := eventHighWater(ctx, storage, &metrics.eventPages)
		if err != nil {
			return 0, err
		}
		_, createErr := createAttempt()
		details, typed := store.ExecutionCapacityErrorDetails(createErr)
		if store.ErrorCode(createErr) != store.CodeExecutionCapacityExhausted || !typed || details.Dimension != dimension || details.Scope != scope || details.Actual != expectedActual || details.Limit != expectedLimit {
			return 0, fmt.Errorf("personal-100 %s capacity attempt error=%v code=%q details=%#v", dimension, createErr, store.ErrorCode(createErr), details)
		}
		after, err := eventHighWater(ctx, storage, &metrics.eventPages)
		if err != nil {
			return 0, err
		}
		return after - before, nil
	}

	for projectIndex := 0; projectIndex < personalProjectCount-1; projectIndex++ {
		if err := assign(projectIndex); err != nil {
			return proof, err
		}
	}
	reconciliationAssigned, err := assignTask(state.projects[9].phaseTask, state.phaseAgents[9], "load-reconciliation-assign-09", 60)
	if err != nil {
		return proof, fmt.Errorf("assign personal-100 controlled reconciliation task: %w", err)
	}
	assigned[9] = reconciliationAssigned
	providerRefusalAssigned, err := assignTask(state.providerRefusalTask, state.providerRefusalAgent, "load-capacity-provider-refusal-assign", 3600)
	if err != nil {
		return proof, fmt.Errorf("assign personal-100 provider refusal task: %w", err)
	}

	for firstProject := 0; firstProject < 4; firstProject += 2 {
		first, err := create(firstProject)
		if err != nil {
			return proof, fmt.Errorf("create personal-100 provider-A run %d: %w", firstProject, err)
		}
		second, err := create(firstProject + 1)
		if err != nil {
			return proof, fmt.Errorf("create personal-100 provider-A run %d: %w", firstProject+1, err)
		}
		if _, err := observeHealth(); err != nil {
			return proof, err
		}
		if firstProject == 0 {
			proof.startingRefusalEventDelta, err = refusal(func() (store.RunMutationResult, error) { return create(8) }, "workspace_starting", state.workspace.ID, 2, 2)
			if err != nil {
				return proof, err
			}
		}
		if err := activate(firstProject, first); err != nil {
			return proof, err
		}
		if err := activate(firstProject+1, second); err != nil {
			return proof, err
		}
	}
	providerHealth, err := observeHealth()
	if err != nil {
		return proof, err
	}
	if executionProviderUnresolved(providerHealth, "fixture-a") != 4 {
		return proof, fmt.Errorf("personal-100 provider A did not saturate: %#v", providerHealth.Providers)
	}
	proof.providerRefusalEventDelta, err = refusal(func() (store.RunMutationResult, error) {
		return createTaskRun(providerRefusalAssigned, state.providerRefusalAgent, state.projects[0].checkout, "load-capacity-provider-refusal-run")
	}, "provider_unresolved", "fixture-a", 4, 4)
	if err != nil {
		return proof, err
	}

	for _, projectIndex := range []int{4, 5} {
		created, err := create(projectIndex)
		if err != nil {
			return proof, fmt.Errorf("create personal-100 provider-B run %d while provider A is saturated: %w", projectIndex, err)
		}
		proof.providerBProgress = 1
		if err := activate(projectIndex, created); err != nil {
			return proof, err
		}
	}
	pendingSix, err := create(6)
	if err != nil {
		return proof, fmt.Errorf("create personal-100 provider-B run 6: %w", err)
	}
	pendingSeven, err := create(7)
	if err != nil {
		return proof, fmt.Errorf("create personal-100 provider-B run 7: %w", err)
	}
	saturated, err := observeHealth()
	if err != nil {
		return proof, err
	}
	proof.saturatedProviderA = executionProviderUnresolved(saturated, "fixture-a")
	proof.saturatedProviderB = executionProviderUnresolved(saturated, "fixture-b")
	proof.saturatedStarting = saturated.Node.Starting
	if saturated.Node.Unresolved != 8 || proof.saturatedProviderA != 4 || proof.saturatedProviderB != 4 || proof.saturatedStarting != 2 {
		return proof, fmt.Errorf("personal-100 saturated control cut differs: node=%#v providers=%#v", saturated.Node, saturated.Providers)
	}
	if err := exerciseSaturatedControl(ctx, storage, root, state, metrics, clock, &proof); err != nil {
		return proof, err
	}
	if err := activate(6, pendingSix); err != nil {
		return proof, err
	}
	if err := activate(7, pendingSeven); err != nil {
		return proof, err
	}
	peak, err := observeHealth()
	if err != nil {
		return proof, err
	}
	if peak.Node.Unresolved != 8 || peak.Node.Starting != 0 || len(peak.Workspaces) != 1 || peak.Workspaces[0].Unresolved != 8 {
		return proof, fmt.Errorf("personal-100 peak capacity=%#v", peak.Node)
	}
	proof.unresolvedRefusalEventDelta, err = refusal(func() (store.RunMutationResult, error) { return create(8) }, "workspace_unresolved", state.workspace.ID, 8, 8)
	if err != nil {
		return proof, err
	}

	for projectIndex, run := range activeRuns {
		detail, err := measured(&metrics.mutations, func() (domain.RunDetail, error) {
			return storage.ApplyRunObservation(ctx, run.ID, domain.RunObservation{
				Kind: domain.ObservationCompletion, Message: "provider-free capacity phase complete", Handoff: "bounded capacity phase settled",
				LogUnavailableReason: "provider-free Store-only capacity phase has no external runtime logs",
			}, true, []string{}, fmt.Sprintf("load-capacity-run-%02d-completed", projectIndex))
		})
		if err != nil {
			return proof, fmt.Errorf("complete personal-100 capacity run %d: %w", projectIndex, err)
		}
		if detail.Run.Status != domain.RunCompleted || detail.Run.RuntimeHandle != "" || detail.Run.ProviderHandle != "" {
			return proof, fmt.Errorf("personal-100 capacity run %d did not terminalize", projectIndex)
		}
	}
	for _, pending := range []struct {
		task domain.Task
		key  string
	}{
		{task: assigned[8], key: "load-capacity-ninth-cancel"},
		{task: providerRefusalAssigned, key: "load-capacity-provider-refusal-cancel"},
	} {
		cancelled, cancelErr := measured(&metrics.mutations, func() (store.TaskMutationResult, error) {
			return storage.TransitionTask(ctx, store.TransitionTaskCommand{
				WorkspaceIdentifier: state.workspace.ID, TaskID: pending.task.ID, Action: "cancel", ExpectedRevision: pending.task.Revision,
				IdempotencyKey: pending.key, CorrelationID: pending.key + "-request",
			})
		})
		if cancelErr != nil || cancelled.Detail.Task.Status != domain.TaskCancelled {
			return proof, fmt.Errorf("cancel refused personal-100 capacity task: %w", cancelErr)
		}
	}
	settled, err := observeHealth()
	if err != nil {
		return proof, err
	}
	proof.settledUnresolved, proof.settledStarting = settled.Node.Unresolved, settled.Node.Starting
	if proof.settledUnresolved != 0 || proof.settledStarting != 0 {
		return proof, fmt.Errorf("personal-100 capacity did not settle: %#v", settled.Node)
	}
	return proof, nil
}

func eventHighWater(ctx context.Context, storage *store.Store, samples *[]time.Duration) (int64, error) {
	page, err := measured(samples, func() (store.EventPage, error) {
		return storage.ListEvents(ctx, store.ListEventsQuery{WorkspaceIdentifier: "personal-100", Limit: 1})
	})
	if err != nil {
		return 0, err
	}
	return page.HighWater, nil
}

func personalObservation(checkoutPath, gitPath string, index int) domain.CheckoutObservation {
	repositoryDigest := sha256.Sum256([]byte(fmt.Sprintf("personal-100-repository-%02d", index)))
	rootDigest := sha256.Sum256([]byte(fmt.Sprintf("personal-100-root-%02d", index)))
	headDigest := sha256.Sum256([]byte(fmt.Sprintf("personal-100-head-%02d", index)))
	return domain.CheckoutObservation{
		Path:         checkoutPath,
		Availability: domain.CheckoutAvailable,
		CheckoutKind: domain.CheckoutStandalone,
		Branch:       "load",
		HeadCommit:   hex.EncodeToString(headDigest[:])[:40],
		DirtyPaths:   []string{},
		GitDir:       gitPath,
		GitCommonDir: gitPath,
		Repository: domain.RepositoryObservation{
			Fingerprint:  "git_" + hex.EncodeToString(repositoryDigest[:]),
			ObjectFormat: "sha1",
			RootCommits:  []string{hex.EncodeToString(rootDigest[:])[:40]},
		},
	}
}

func createLoadCheckpoint(ctx context.Context, storage *store.Store, workspaceID, projectID string, index int64, samples *[]time.Duration) error {
	key := "load-checkpoint-" + strconv.FormatInt(index, 10)
	_, err := measured(samples, func() (store.OwnerCheckpointMutationResult, error) {
		return storage.CreateOwnerCheckpoint(ctx, store.CreateOwnerCheckpointCommand{
			WorkspaceIdentifier: workspaceID,
			ScopeType:           domain.OwnerCheckpointProject,
			ScopeIdentifier:     projectID,
			IdempotencyKey:      key,
			CorrelationID:       key + "-request",
		})
	})
	if err != nil {
		return fmt.Errorf("create personal-100 checkpoint %d: %w", index, err)
	}
	return nil
}

func personalAssertions(report Report, unownedEvents, launchProfiles, runs int64, capacity capacityProof, briefing briefingProof) []Assertion {
	totalMicroseconds := durationMicroseconds(time.Duration(report.Timings[0].MaxMicroseconds+report.Timings[1].MaxMicroseconds) * time.Microsecond)
	projectTiming := findTiming(report.Timings, "project_briefing")
	workspaceTiming := findTiming(report.Timings, "workspace_briefing")
	warmStartupTiming := findTiming(report.Timings, "warm_startup")
	statusTiming := findTiming(report.Timings, "saturated_status")
	messageTiming := findTiming(report.Timings, "saturated_message")
	controlTiming := findTiming(report.Timings, "saturated_control")
	reconciliationTiming := findTiming(report.Timings, "lease_reconciliation")
	doctorTiming := findTiming(report.Timings, "doctor_full")
	backupCreateTiming := findTiming(report.Timings, "backup_create")
	backupVerifyTiming := findTiming(report.Timings, "backup_verify")
	backupRestoreTiming := findTiming(report.Timings, "backup_restore")
	return []Assertion{
		equalityAssertion("workspaces", report.Counts.Workspaces, personalWorkspaceCount, "count"),
		equalityAssertion("projects", report.Counts.Projects, personalProjectCount, "count"),
		equalityAssertion("agents", report.Counts.Agents, personalAgentCount, "count"),
		equalityAssertion("objectives", report.Counts.Objectives, personalObjectiveCount, "count"),
		equalityAssertion("tasks", report.Counts.Tasks, personalTaskCount, "count"),
		equalityAssertion("known_events", report.Counts.KnownEvents, personalEventCount, "count"),
		equalityAssertion("noisy_project_events", report.Counts.NoisyProjectEvents, personalNoisyEventCount, "count"),
		equalityAssertion("unowned_workspace_agent_events", unownedEvents, personalUnownedEventCount, "count"),
		equalityAssertion("launch_profiles", launchProfiles, personalLaunchProfileCount, "count"),
		equalityAssertion("terminal_runs", runs, 8, "count"),
		equalityAssertion("peak_unresolved_runs", capacity.peakUnresolved, 8, "count"),
		equalityAssertion("peak_starting_runs", capacity.peakStarting, 2, "count"),
		equalityAssertion("peak_provider_unresolved_runs", capacity.peakProviderUnresolved, 4, "count"),
		equalityAssertion("peak_project_unresolved_runs", capacity.peakProjectUnresolved, 1, "count"),
		equalityAssertion("starting_refusal_event_delta", capacity.startingRefusalEventDelta, 0, "events"),
		equalityAssertion("provider_refusal_event_delta", capacity.providerRefusalEventDelta, 0, "events"),
		equalityAssertion("unresolved_refusal_event_delta", capacity.unresolvedRefusalEventDelta, 0, "events"),
		equalityAssertion("provider_b_progress", capacity.providerBProgress, 1, "count"),
		equalityAssertion("saturated_provider_a_runs", capacity.saturatedProviderA, 4, "count"),
		equalityAssertion("saturated_provider_b_runs", capacity.saturatedProviderB, 4, "count"),
		equalityAssertion("saturated_starting_runs", capacity.saturatedStarting, 2, "count"),
		equalityAssertion("saturated_control_event_delta", capacity.controlEventDelta, personalControlEventDelta, "events"),
		equalityAssertion("asynchronous_wake_blocked", capacity.asyncWakeBlocked, 1, "count"),
		equalityAssertion("reconciliation_event_delta", capacity.reconciliationEventDelta, 1, "events"),
		equalityAssertion("reconciliation_settled", capacity.reconciliationSettled, 1, "count"),
		equalityAssertion("settled_unresolved_runs", capacity.settledUnresolved, 0, "count"),
		equalityAssertion("settled_starting_runs", capacity.settledStarting, 0, "count"),
		equalityAssertion("saturated_status_operations", int64(statusTiming.Repetitions), personalStatusOperations, "count"),
		equalityAssertion("saturated_message_operations", int64(messageTiming.Repetitions), personalMessageOperations, "count"),
		equalityAssertion("saturated_control_operations", int64(controlTiming.Repetitions), personalControlOperations, "count"),
		maximumAssertion("saturated_status_p99", statusTiming.P99Microseconds, personalControlP99.Microseconds(), "microseconds"),
		maximumAssertion("saturated_status_max", statusTiming.MaxMicroseconds, personalControlMax.Microseconds(), "microseconds"),
		maximumAssertion("saturated_message_p99", messageTiming.P99Microseconds, personalControlP99.Microseconds(), "microseconds"),
		maximumAssertion("saturated_message_max", messageTiming.MaxMicroseconds, personalControlMax.Microseconds(), "microseconds"),
		maximumAssertion("saturated_control_p99", controlTiming.P99Microseconds, personalControlP99.Microseconds(), "microseconds"),
		maximumAssertion("saturated_control_max", controlTiming.MaxMicroseconds, personalControlMax.Microseconds(), "microseconds"),
		equalityAssertion("project_briefing_reads", int64(projectTiming.Repetitions), personalBriefingReads, "count"),
		equalityAssertion("workspace_briefing_reads", int64(workspaceTiming.Repetitions), personalBriefingReads, "count"),
		maximumAssertion("project_briefing_claims", briefing.projectClaims, personalBriefingClaims, "count"),
		maximumAssertion("workspace_briefing_claims", briefing.workspaceClaims, personalBriefingClaims, "count"),
		equalityAssertion("project_briefing_source_accounting", briefing.projectClaims+briefing.projectOmitted, personalNoisyCommitments, "count"),
		equalityAssertion("workspace_briefing_source_accounting", briefing.workspaceClaims+briefing.workspaceOmitted, personalCommitmentCount, "count"),
		maximumAssertion("project_briefing_bytes", briefing.projectBytes, personalBriefingBytes, "bytes"),
		maximumAssertion("workspace_briefing_bytes", briefing.workspaceBytes, personalBriefingBytes, "bytes"),
		equalityAssertion("quiet_project_briefing_fairness", briefing.quietVisible, personalQuietCommitments, "count"),
		equalityAssertion("project_briefing_reuse", briefing.projectReused, personalBriefingReads-1, "count"),
		equalityAssertion("workspace_briefing_reuse", briefing.workspaceReused, personalBriefingReads-1, "count"),
		maximumAssertion("project_briefing_p99", projectTiming.P99Microseconds, personalProjectBriefingP99.Microseconds(), "microseconds"),
		maximumAssertion("project_briefing_max", projectTiming.MaxMicroseconds, personalProjectBriefingMax.Microseconds(), "microseconds"),
		maximumAssertion("workspace_briefing_p99", workspaceTiming.P99Microseconds, personalWorkspaceBriefingP99.Microseconds(), "microseconds"),
		maximumAssertion("workspace_briefing_max", workspaceTiming.MaxMicroseconds, personalWorkspaceBriefingMax.Microseconds(), "microseconds"),
		maximumAssertion("generation_and_verification", totalMicroseconds, personalMaximumDuration.Microseconds(), "microseconds"),
		maximumObservedAssertion("warm_daemon_startup", warmStartupTiming.MaxMicroseconds, personalWarmStartupMax.Microseconds(), "microseconds"),
		maximumObservedAssertion("lease_reconciliation", reconciliationTiming.MaxMicroseconds, personalReconciliationMax.Microseconds(), "microseconds"),
		maximumObservedAssertion("doctor_full", doctorTiming.MaxMicroseconds, personalRecoveryOperationMax.Microseconds(), "microseconds"),
		maximumObservedAssertion("backup_create", backupCreateTiming.MaxMicroseconds, personalRecoveryOperationMax.Microseconds(), "microseconds"),
		maximumObservedAssertion("backup_verify", backupVerifyTiming.MaxMicroseconds, personalRecoveryOperationMax.Microseconds(), "microseconds"),
		maximumObservedAssertion("backup_restore", backupRestoreTiming.MaxMicroseconds, personalRecoveryOperationMax.Microseconds(), "microseconds"),
		minimumAssertion("peak_rss_observed", int64(report.Resources.PeakRSSBytes), 1, "bytes"),
		maximumAssertion("peak_rss", int64(report.Resources.PeakRSSBytes), personalMaximumRSS, "bytes"),
		maximumAssertion("database_and_artifacts", report.Resources.DatabaseBytes+report.Resources.ArtifactBytes, personalMaximumBytes, "bytes"),
	}
}

func findTiming(values []Timing, name string) Timing {
	for _, value := range values {
		if value.Name == name {
			return value
		}
	}
	return Timing{Name: name}
}

func equalityAssertion(name string, actual, expected int64, unit string) Assertion {
	return Assertion{Name: name, Passed: actual == expected, Actual: actual, Limit: expected, Unit: unit}
}

func maximumAssertion(name string, actual, limit int64, unit string) Assertion {
	return Assertion{Name: name, Passed: actual <= limit, Actual: actual, Limit: limit, Unit: unit}
}

func maximumObservedAssertion(name string, actual, limit int64, unit string) Assertion {
	return Assertion{Name: name, Passed: actual > 0 && actual <= limit, Actual: actual, Limit: limit, Unit: unit}
}

func minimumAssertion(name string, actual, minimum int64, unit string) Assertion {
	return Assertion{Name: name, Passed: actual >= minimum, Actual: actual, Limit: minimum, Unit: unit}
}

func fixtureArtifactBytes(root string) (int64, error) {
	var total int64
	for _, name := range []string{"check-artifacts", "run-artifacts"} {
		base := filepath.Join(root, name)
		info, err := os.Lstat(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("inspect personal-100 artifact directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("personal-100 artifact path %s is not a directory", name)
		}
		err = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("personal-100 artifact tree contains a symlink")
			}
			if entry.Type().IsRegular() {
				item, statErr := entry.Info()
				if statErr != nil {
					return statErr
				}
				total += item.Size()
			}
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("measure personal-100 artifacts: %w", err)
		}
	}
	return total, nil
}

func entityMapKey(entityType, id string) string {
	return entityType + "\x00" + id
}

func measured[T any](samples *[]time.Duration, operation func() (T, error)) (T, error) {
	started := time.Now()
	result, err := operation()
	*samples = append(*samples, time.Since(started))
	return result, err
}
