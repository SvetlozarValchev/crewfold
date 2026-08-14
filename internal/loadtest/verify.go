package loadtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"sort"
	"strconv"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store"
)

type verificationResult struct {
	counts         Counts
	logicalSHA256  string
	unownedEvents  int64
	launchProfiles int64
	runs           int64
}

type logicalIndex struct {
	entityKeys    map[string]string
	projectOwners map[string]string
	projectIDs    map[string]string
}

func verifyPersonal100(ctx context.Context, storage *store.Store, labels labelSet, metrics *measurements) (verificationResult, error) {
	result := verificationResult{}
	logicalHash := sha256.New()
	index := logicalIndex{
		entityKeys:    make(map[string]string, 1400),
		projectOwners: make(map[string]string, 1300),
		projectIDs:    make(map[string]string, personalProjectCount),
	}

	workspaces, err := readWorkspaces(ctx, storage, &metrics.projectionReads)
	if err != nil {
		return result, err
	}
	result.counts.Workspaces = int64(len(workspaces))
	if len(workspaces) != personalWorkspaceCount || workspaces[0].Name != "personal-100" {
		return result, fmt.Errorf("personal-100 workspace projection differs: count=%d", len(workspaces))
	}
	workspace := workspaces[0]
	index.entityKeys[entityMapKey("workspace", workspace.ID)] = "workspace/personal-100"
	writeLogical(logicalHash, "workspace", workspace.Name)

	projects, err := readProjects(ctx, storage, workspace.ID, &metrics.projectionReads)
	if err != nil {
		return result, err
	}
	result.counts.Projects = int64(len(projects))
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	if len(projects) != personalProjectCount {
		return result, fmt.Errorf("personal-100 project projection count=%d", len(projects))
	}
	for projectIndex, project := range projects {
		expectedName := fmt.Sprintf("project-%02d", projectIndex)
		if project.Name != expectedName || project.WorkspaceID != workspace.ID {
			return result, fmt.Errorf("personal-100 project %d is %q", projectIndex, project.Name)
		}
		logicalProject := "project/" + project.Name
		index.entityKeys[entityMapKey("project", project.ID)] = logicalProject
		index.projectOwners[entityMapKey("project", project.ID)] = logicalProject
		index.projectIDs[project.ID] = logicalProject
		writeLogical(logicalHash, "project", project.Name)

		inspection, err := measured(&metrics.projectionReads, func() (store.ProjectInspection, error) {
			return storage.InspectProject(ctx, workspace.ID, project.ID)
		})
		if err != nil {
			return result, fmt.Errorf("inspect personal-100 project %s: %w", project.Name, err)
		}
		if len(inspection.Repositories) != 1 || len(inspection.Checkouts) != 1 {
			return result, fmt.Errorf("personal-100 project %s has repositories=%d checkouts=%d", project.Name, len(inspection.Repositories), len(inspection.Checkouts))
		}
		repository := inspection.Repositories[0]
		checkout := inspection.Checkouts[0]
		if checkout.ProjectID != project.ID || checkout.RepositoryID != repository.ID || checkout.WriteMode != domain.WriteModeExclusive || checkout.Availability != domain.CheckoutAvailable || checkout.CheckoutKind != domain.CheckoutStandalone {
			return result, fmt.Errorf("personal-100 project %s checkout binding differs", project.Name)
		}
		index.entityKeys[entityMapKey("repository", repository.ID)] = logicalProject + "/repository"
		index.entityKeys[entityMapKey("checkout", checkout.ID)] = logicalProject + "/checkout"
		index.projectOwners[entityMapKey("repository", repository.ID)] = logicalProject
		index.projectOwners[entityMapKey("checkout", checkout.ID)] = logicalProject
		writeLogical(logicalHash, "repository", logicalProject, repository.Fingerprint, repository.ObjectFormat, stringsJoined(repository.RootCommits))
		writeLogical(logicalHash, "checkout", logicalProject, checkout.WriteMode, checkout.Availability, checkout.CheckoutKind, checkout.Branch, checkout.HeadCommit)
	}

	agents, err := readAgents(ctx, storage, workspace.ID, &metrics.projectionReads)
	if err != nil {
		return result, err
	}
	result.counts.Agents = int64(len(agents))
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	if len(agents) != personalAgentCount {
		return result, fmt.Errorf("personal-100 agent projection count=%d", len(agents))
	}
	agentNames := make(map[string]string, len(agents))
	agentsByID := make(map[string]domain.AgentDefinition, len(agents))
	for agentIndex, agent := range agents {
		expectedName := fmt.Sprintf("agent-%03d", agentIndex)
		expectedProvider := personalProvider(agentIndex % personalProjectCount)
		if agent.Name != expectedName || agent.WorkspaceID != workspace.ID || agent.Role != labels.rolePrefix+"-"+expectedName || agent.Provider != expectedProvider || agent.Runtime != "fixture" || !agent.Enabled || agent.MaxConcurrency != 1 || agent.Revision != 1 {
			return result, fmt.Errorf("personal-100 agent %d differs from %s", agentIndex, expectedName)
		}
		agentNames[agent.ID] = expectedName
		agentsByID[agent.ID] = agent
		index.entityKeys[entityMapKey("agent", agent.ID)] = "agent/" + expectedName
		// Role is display metadata and is deliberately absent from this hash.
		writeLogical(logicalHash, "agent", expectedName, agent.Provider, agent.Runtime, strconv.FormatBool(agent.Enabled), strconv.Itoa(agent.MaxConcurrency))
	}

	profiles := make([]domain.LaunchProfile, 0, personalLaunchProfileCount)
	for _, project := range projects {
		projectProfiles, err := measured(&metrics.projectionReads, func() ([]domain.LaunchProfile, error) {
			return storage.LaunchProfiles(ctx, store.ListLaunchProfilesQuery{
				WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Limit: 100,
			})
		})
		if err != nil {
			return result, fmt.Errorf("list personal-100 launch profiles for %s: %w", project.Name, err)
		}
		if len(projectProfiles) != personalAgentCount/personalProjectCount {
			return result, fmt.Errorf("personal-100 project %s launch profile count=%d", project.Name, len(projectProfiles))
		}
		profiles = append(profiles, projectProfiles...)
	}
	result.launchProfiles = int64(len(profiles))
	sort.Slice(profiles, func(i, j int) bool { return agentNames[profiles[i].AgentID] < agentNames[profiles[j].AgentID] })
	for _, profile := range profiles {
		agentName, exists := agentNames[profile.AgentID]
		agent := agentsByID[profile.AgentID]
		logicalProject, projectExists := index.projectIDs[profile.ProjectID]
		if !exists || !projectExists || profile.Purpose != labels.purposePrefix+"-"+agentName || profile.Runtime != agent.Runtime || profile.Provider != agent.Provider || profile.Status != domain.LaunchProfileActive || profile.Revision != 1 || profile.AssignmentLeaseSeconds != 300 || profile.CapabilityTTLSeconds != 300 || profile.ManagerGrantID != "" || profile.Scenario.Schema != personalScenarioSchema || profile.Scenario.Name != "personal-load" {
			return result, fmt.Errorf("personal-100 launch profile for %s differs", agentName)
		}
		index.entityKeys[entityMapKey("launch_profile", profile.ID)] = logicalProject + "/profile/" + agentName
		index.projectOwners[entityMapKey("launch_profile", profile.ID)] = logicalProject
		// Purpose and content hash change with descriptive wording and are
		// intentionally absent; exact runtime/provider/scenario bindings remain.
		writeLogical(logicalHash, "launch_profile", logicalProject, agentName, profile.Runtime, profile.Provider, profile.Scenario.Name, strconv.FormatInt(profile.AssignmentLeaseSeconds, 10), strconv.FormatInt(profile.CapabilityTTLSeconds, 10), profile.Status)
	}

	objectives, err := readObjectives(ctx, storage, workspace.ID, &metrics.projectionReads)
	if err != nil {
		return result, err
	}
	result.counts.Objectives = int64(len(objectives))
	sort.Slice(objectives, func(i, j int) bool { return objectives[i].Title < objectives[j].Title })
	if len(objectives) != personalObjectiveCount {
		return result, fmt.Errorf("personal-100 objective projection count=%d", len(objectives))
	}
	objectiveKeys := make(map[string]string, len(objectives))
	for objectiveIndex, objective := range objectives {
		expectedTitle := fmt.Sprintf("objective-%02d", objectiveIndex)
		logicalProject, exists := index.projectIDs[objective.ProjectID]
		if !exists || objective.Title != expectedTitle || objective.Status != domain.ObjectiveActive || objective.Budget != (domain.Budget{}) || objective.Revision != 1 {
			return result, fmt.Errorf("personal-100 objective %d differs from %s", objectiveIndex, expectedTitle)
		}
		logicalObjective := logicalProject + "/objective"
		objectiveKeys[objective.ID] = logicalObjective
		index.entityKeys[entityMapKey("objective", objective.ID)] = logicalObjective
		index.projectOwners[entityMapKey("objective", objective.ID)] = logicalProject
		writeLogical(logicalHash, "objective", logicalProject, objective.Title, objective.Status)
	}

	tasks, err := readTasks(ctx, storage, workspace.ID, &metrics.projectionReads)
	if err != nil {
		return result, err
	}
	result.counts.Tasks = int64(len(tasks))
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Task.Title < tasks[j].Task.Title })
	if len(tasks) != personalTaskCount {
		return result, fmt.Errorf("personal-100 task projection count=%d", len(tasks))
	}
	tasksByTitle := make(map[string]domain.Task, len(tasks))
	for taskIndex, detail := range tasks {
		projectIndex := taskIndex / (personalTaskCount / personalProjectCount)
		withinProject := taskIndex % (personalTaskCount / personalProjectCount)
		expectedTitle := fmt.Sprintf("task-%02d-%03d", projectIndex, withinProject)
		task := detail.Task
		logicalProject, projectExists := index.projectIDs[task.ProjectID]
		logicalObjective, objectiveExists := objectiveKeys[task.ObjectiveID]
		expectedStatus := domain.TaskReady
		if withinProject == 0 && projectIndex < 8 {
			expectedStatus = domain.TaskCompleted
		} else if withinProject == 0 && projectIndex == 8 {
			expectedStatus = domain.TaskCancelled
		}
		if !projectExists || !objectiveExists || task.Title != expectedTitle || task.Description != "deterministic provider-free personal load task" || task.Status != expectedStatus || task.Priority != withinProject%11 || task.Budget != (domain.Budget{}) || len(detail.Dependencies) != 0 || detail.Assignment != nil {
			return result, fmt.Errorf("personal-100 task %d differs from %s", taskIndex, expectedTitle)
		}
		logicalTask := logicalProject + "/task/" + task.Title
		index.entityKeys[entityMapKey("task", task.ID)] = logicalTask
		index.projectOwners[entityMapKey("task", task.ID)] = logicalProject
		tasksByTitle[task.Title] = task
		writeLogical(logicalHash, "task", logicalProject, logicalObjective, task.Title, task.Description, task.Status, strconv.Itoa(task.Priority))
	}
	if err := verifyPersonalCommitments(ctx, storage, workspace, projects, tasksByTitle, index, logicalHash, &metrics.projectionReads); err != nil {
		return result, err
	}

	runs, boundRuns, err := readRuns(ctx, storage, workspace.ID, &metrics.projectionReads)
	if err != nil {
		return result, err
	}
	result.runs = int64(len(runs))
	if len(runs) != 8 {
		return result, fmt.Errorf("personal-100 terminal run count=%d", len(runs))
	}
	sort.Slice(runs, func(i, j int) bool {
		return index.entityKeys[entityMapKey("task", runs[i].TaskID)] < index.entityKeys[entityMapKey("task", runs[j].TaskID)]
	})
	for _, summary := range runs {
		logicalProject, projectExists := index.projectIDs[summary.ProjectID]
		logicalTask, taskExists := index.entityKeys[entityMapKey("task", summary.TaskID)]
		agent, agentExists := agentsByID[summary.AgentID]
		if !projectExists || !taskExists || !agentExists || summary.Status != domain.RunCompleted || summary.Runtime != agent.Runtime || summary.Provider != agent.Provider || boundRuns[summary.ID] {
			return result, fmt.Errorf("personal-100 terminal run %s differs", summary.ID)
		}
		detail, err := measured(&metrics.projectionReads, func() (domain.RunDetail, error) {
			return storage.RunDetail(ctx, workspace.ID, summary.ID)
		})
		if err != nil {
			return result, fmt.Errorf("read personal-100 terminal run %s: %w", summary.ID, err)
		}
		if detail.Run.ContextPacketID == "" || detail.Run.RuntimeHandle != "" || detail.Run.ProviderHandle != "" || detail.Handoff == nil || detail.Task.Status != domain.TaskCompleted {
			return result, fmt.Errorf("personal-100 terminal run %s retained live state", summary.ID)
		}
		packet, err := measured(&metrics.projectionReads, func() (domain.ContextPacket, error) {
			return storage.ContextPacket(ctx, workspace.ID, detail.Run.ContextPacketID)
		})
		if err != nil {
			return result, fmt.Errorf("read personal-100 context packet for %s: %w", summary.ID, err)
		}
		if packet.WorkspaceID != workspace.ID || packet.ProjectID != summary.ProjectID || packet.TaskID != summary.TaskID || packet.AgentID != summary.AgentID || packet.CheckoutID != detail.Run.CheckoutID {
			return result, fmt.Errorf("personal-100 context packet for %s differs", summary.ID)
		}
		logicalRun := logicalTask + "/run"
		index.entityKeys[entityMapKey("run", summary.ID)] = logicalRun
		index.entityKeys[entityMapKey("context_packet", packet.ID)] = logicalTask + "/context"
		index.projectOwners[entityMapKey("run", summary.ID)] = logicalProject
		index.projectOwners[entityMapKey("context_packet", packet.ID)] = logicalProject
		writeLogical(logicalHash, "run", logicalProject, logicalTask, agentNames[summary.AgentID], summary.Runtime, summary.Provider, summary.Status, detail.Run.ScenarioName)
		// Context Role and content hash include descriptive Role wording; exact
		// scope/binding facts remain in the neutral topology hash.
		writeLogical(logicalHash, "context_packet", logicalProject, logicalTask, agentNames[summary.AgentID], detail.Checkout.WriteMode, packet.Schema)
	}

	eventCounts, err := verifyEventStream(ctx, storage, index, logicalHash, &metrics.eventPages)
	if err != nil {
		return result, err
	}
	result.counts.KnownEvents = eventCounts.total
	result.counts.NoisyProjectEvents = eventCounts.byProject["project/project-00"]
	result.unownedEvents = eventCounts.unowned
	for _, project := range projects[1:] {
		if eventCounts.byProject["project/"+project.Name] == 0 {
			return result, fmt.Errorf("quiet personal-100 project %s owns no event", project.Name)
		}
	}
	if eventCounts.owned+eventCounts.unowned != eventCounts.total {
		return result, fmt.Errorf("personal-100 event ownership is not exhaustive: owned=%d unowned=%d total=%d", eventCounts.owned, eventCounts.unowned, eventCounts.total)
	}
	result.logicalSHA256 = hex.EncodeToString(logicalHash.Sum(nil))
	return result, nil
}

func verifyPersonalCommitments(ctx context.Context, storage *store.Store, workspace domain.Workspace, projects []domain.Project, tasksByTitle map[string]domain.Task, index logicalIndex, logicalHash hash.Hash, samples *[]time.Duration) error {
	verifyBatch := func(projectIndex, firstIndex, expectedCount int, task domain.Task, quiet bool) error {
		commitments, err := measured(samples, func() ([]domain.DeliverableCommitment, error) {
			return storage.DeliverableCommitments(ctx, store.ListDeliverableCommitmentsQuery{
				WorkspaceIdentifier: workspace.ID,
				TaskID:              task.ID,
				Limit:               100,
			})
		})
		if err != nil {
			return fmt.Errorf("list personal-100 commitments for %s: %w", task.Title, err)
		}
		if len(commitments) != expectedCount {
			return fmt.Errorf("personal-100 commitment count for %s=%d, want %d", task.Title, len(commitments), expectedCount)
		}
		project := projects[projectIndex]
		logicalProject := "project/" + project.Name
		for offset, commitment := range commitments {
			ordinal := firstIndex + offset
			key := fmt.Sprintf("noisy-%03d", ordinal)
			title := fmt.Sprintf("Noisy urgent fact %03d", ordinal)
			if quiet {
				key = fmt.Sprintf("quiet-%02d", projectIndex)
				title = fmt.Sprintf("Quiet urgent fact %02d", projectIndex)
			}
			contentHash, contentHashErr := hex.DecodeString(commitment.ContentSHA256)
			if commitment.WorkspaceID != workspace.ID || commitment.ProjectID != project.ID ||
				commitment.ObjectiveID != task.ObjectiveID || commitment.TaskID != task.ID ||
				commitment.Key != key || commitment.Title != title ||
				commitment.Description != "provider-free personal-scale owner decision" ||
				len(commitment.AcceptanceCriteria) != 1 || commitment.AcceptanceCriteria[0] != "the owner accepts or explicitly resolves this current delivery fact" ||
				contentHashErr != nil || len(contentHash) != sha256.Size {
				return fmt.Errorf("personal-100 commitment %s differs", key)
			}
			logicalCommitment := logicalProject + "/commitment/" + key
			index.entityKeys[entityMapKey("deliverable_commitment", commitment.ID)] = logicalCommitment
			index.projectOwners[entityMapKey("deliverable_commitment", commitment.ID)] = logicalProject
			writeLogical(logicalHash, "deliverable_commitment", logicalProject, index.entityKeys[entityMapKey("task", task.ID)], key, title, commitment.Description, stringsJoined(commitment.AcceptanceCriteria))
		}
		return nil
	}

	for taskOffset := 0; taskOffset < 2; taskOffset++ {
		taskTitle := fmt.Sprintf("task-00-%03d", taskOffset+1)
		task, exists := tasksByTitle[taskTitle]
		if !exists {
			return fmt.Errorf("personal-100 briefing task %s is absent", taskTitle)
		}
		if err := verifyBatch(0, taskOffset*100, 100, task, false); err != nil {
			return err
		}
	}
	for projectIndex := 1; projectIndex < personalProjectCount; projectIndex++ {
		taskTitle := fmt.Sprintf("task-%02d-001", projectIndex)
		task, exists := tasksByTitle[taskTitle]
		if !exists {
			return fmt.Errorf("personal-100 briefing task %s is absent", taskTitle)
		}
		if err := verifyBatch(projectIndex, 0, 1, task, true); err != nil {
			return err
		}
	}
	return nil
}

type eventCounts struct {
	total     int64
	owned     int64
	unowned   int64
	byProject map[string]int64
}

func verifyEventStream(ctx context.Context, storage *store.Store, index logicalIndex, logicalHash hash.Hash, samples *[]time.Duration) (eventCounts, error) {
	counts := eventCounts{byProject: make(map[string]int64, personalProjectCount)}
	cursor := ""
	highWater := int64(0)
	checkpointOrdinals := make(map[string]int64, personalProjectCount)
	for {
		page, err := measured(samples, func() (store.EventPage, error) {
			return storage.ListEvents(ctx, store.ListEventsQuery{
				WorkspaceIdentifier: "personal-100", Cursor: cursor, Limit: store.MaximumEventPageLimit,
			})
		})
		if err != nil {
			return counts, fmt.Errorf("read personal-100 known event page: %w", err)
		}
		if cursor == "" {
			highWater = page.HighWater
			if page.Total != personalEventCount || highWater != personalEventCount {
				return counts, fmt.Errorf("personal-100 event interval total=%d high_water=%d", page.Total, highWater)
			}
		} else if page.HighWater != highWater || page.Total != personalEventCount {
			return counts, errors.New("personal-100 event interval changed while paging")
		}
		for _, event := range page.Events {
			if !domain.KnownEventType(event.Type) {
				return counts, fmt.Errorf("personal-100 event %d has unknown current event type %q", event.Sequence, event.Type)
			}
			counts.total++
			if event.Sequence != counts.total || event.WorkspaceID == "" || event.SchemaVersion != 1 {
				return counts, fmt.Errorf("personal-100 event %d has noncanonical sequence/workspace/schema", counts.total)
			}
			logicalEntity, owner, err := logicalEventIdentity(event, index, checkpointOrdinals)
			if err != nil {
				return counts, err
			}
			if owner == "" {
				counts.unowned++
			} else {
				counts.owned++
				counts.byProject[owner]++
			}
			writeLogical(logicalHash, "event", strconv.FormatInt(counts.total, 10), event.Type, strconv.Itoa(event.SchemaVersion), event.Entity.Type, logicalEntity, owner)
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				return counts, errors.New("final personal-100 event page has a cursor")
			}
			break
		}
		if page.NextCursor == "" || len(page.Events) == 0 {
			return counts, errors.New("personal-100 event page cannot advance")
		}
		cursor = page.NextCursor
	}
	return counts, nil
}

func countOwnedEvents(ctx context.Context, storage *store.Store, entityKeys, projectOwners map[string]string, noisyProject string, samples *[]time.Duration) (total, noisy, unowned int64, returnedErr error) {
	index := logicalIndex{entityKeys: entityKeys, projectOwners: projectOwners, projectIDs: make(map[string]string)}
	for key, logical := range entityKeys {
		if len(key) > len("project\x00") && key[:len("project\x00")] == "project\x00" {
			index.projectIDs[key[len("project\x00"):]] = logical
		}
	}
	cursor := ""
	checkpointOrdinals := make(map[string]int64)
	for {
		page, err := measured(samples, func() (store.EventPage, error) {
			return storage.ListEvents(ctx, store.ListEventsQuery{WorkspaceIdentifier: "personal-100", Cursor: cursor, Limit: store.MaximumEventPageLimit})
		})
		if err != nil {
			return 0, 0, 0, err
		}
		for _, event := range page.Events {
			if !domain.KnownEventType(event.Type) {
				return 0, 0, 0, fmt.Errorf("personal-100 base event %d has unknown current event type %q", event.Sequence, event.Type)
			}
			total++
			_, owner, identityErr := logicalEventIdentity(event, index, checkpointOrdinals)
			if identityErr != nil {
				return 0, 0, 0, identityErr
			}
			if owner == "" {
				unowned++
			} else if owner == noisyProject {
				noisy++
			}
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			return 0, 0, 0, errors.New("personal-100 base event page cannot advance")
		}
		cursor = page.NextCursor
	}
	return total, noisy, unowned, nil
}

func logicalEventIdentity(event domain.Event, index logicalIndex, checkpointOrdinals map[string]int64) (logicalEntity, owner string, returnedErr error) {
	if event.Entity.Type == "owner_checkpoint" {
		if event.Type != "owner_checkpoint.created" {
			return "", "", fmt.Errorf("owner checkpoint event has type %q", event.Type)
		}
		var payload struct {
			ScopeType string `json:"scope_type"`
			ScopeID   string `json:"scope_id"`
		}
		decoder := json.NewDecoder(bytes.NewReader(event.Data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return "", "", fmt.Errorf("decode owner checkpoint event %d: %w", event.Sequence, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return "", "", fmt.Errorf("owner checkpoint event %d has trailing data", event.Sequence)
		}
		if payload.ScopeType != domain.OwnerCheckpointProject {
			return "", "", fmt.Errorf("owner checkpoint event %d is not project-scoped", event.Sequence)
		}
		owner, exists := index.projectIDs[payload.ScopeID]
		if !exists {
			return "", "", fmt.Errorf("owner checkpoint event %d names an unknown project", event.Sequence)
		}
		checkpointOrdinals[owner]++
		return owner + "/checkpoint/" + strconv.FormatInt(checkpointOrdinals[owner], 10), owner, nil
	}
	key := entityMapKey(event.Entity.Type, event.Entity.ID)
	logicalEntity, exists := index.entityKeys[key]
	if !exists {
		return "", "", fmt.Errorf("personal-100 event %d has unmapped %s entity", event.Sequence, event.Entity.Type)
	}
	return logicalEntity, index.projectOwners[key], nil
}

func readWorkspaces(ctx context.Context, storage *store.Store, samples *[]time.Duration) ([]domain.Workspace, error) {
	values := make([]domain.Workspace, 0, personalWorkspaceCount)
	cursor := ""
	for {
		page, err := measured(samples, func() (store.WorkspacePage, error) {
			return storage.ListWorkspaces(ctx, store.ListWorkspacesQuery{Cursor: cursor, Limit: store.MaximumReadPageLimit})
		})
		if err != nil {
			return nil, fmt.Errorf("list personal-100 workspaces: %w", err)
		}
		values = append(values, page.Workspaces...)
		if !page.HasMore {
			return values, nil
		}
		cursor = page.NextCursor
	}
}

func readProjects(ctx context.Context, storage *store.Store, workspaceID string, samples *[]time.Duration) ([]domain.Project, error) {
	values := make([]domain.Project, 0, personalProjectCount)
	cursor := ""
	for {
		page, err := measured(samples, func() (store.ProjectPage, error) {
			return storage.ListProjects(ctx, store.ListProjectsQuery{WorkspaceIdentifier: workspaceID, Cursor: cursor, Limit: store.MaximumReadPageLimit})
		})
		if err != nil {
			return nil, fmt.Errorf("list personal-100 projects: %w", err)
		}
		values = append(values, page.Projects...)
		if !page.HasMore {
			return values, nil
		}
		cursor = page.NextCursor
	}
}

func readAgents(ctx context.Context, storage *store.Store, workspaceID string, samples *[]time.Duration) ([]domain.AgentDefinition, error) {
	values := make([]domain.AgentDefinition, 0, personalAgentCount)
	cursor := ""
	for {
		page, err := measured(samples, func() (store.AgentPage, error) {
			return storage.ListAgents(ctx, store.ListAgentsQuery{WorkspaceIdentifier: workspaceID, Cursor: cursor, Limit: store.MaximumReadPageLimit})
		})
		if err != nil {
			return nil, fmt.Errorf("list personal-100 agents: %w", err)
		}
		values = append(values, page.Agents...)
		if !page.HasMore {
			return values, nil
		}
		cursor = page.NextCursor
	}
}

func readObjectives(ctx context.Context, storage *store.Store, workspaceID string, samples *[]time.Duration) ([]domain.Objective, error) {
	values := make([]domain.Objective, 0, personalObjectiveCount)
	cursor := ""
	for {
		page, err := measured(samples, func() (store.ObjectivePage, error) {
			return storage.ListObjectives(ctx, store.ListObjectivesQuery{WorkspaceIdentifier: workspaceID, Cursor: cursor, Limit: store.MaximumReadPageLimit})
		})
		if err != nil {
			return nil, fmt.Errorf("list personal-100 objectives: %w", err)
		}
		values = append(values, page.Objectives...)
		if !page.HasMore {
			return values, nil
		}
		cursor = page.NextCursor
	}
}

func readTasks(ctx context.Context, storage *store.Store, workspaceID string, samples *[]time.Duration) ([]domain.TaskDetail, error) {
	values := make([]domain.TaskDetail, 0, personalTaskCount)
	cursor := ""
	for {
		page, err := measured(samples, func() (store.TaskPage, error) {
			return storage.ListTasks(ctx, store.ListTasksQuery{WorkspaceIdentifier: workspaceID, Cursor: cursor, Limit: store.MaximumReadPageLimit})
		})
		if err != nil {
			return nil, fmt.Errorf("list personal-100 tasks: %w", err)
		}
		values = append(values, page.Tasks...)
		if !page.HasMore {
			return values, nil
		}
		cursor = page.NextCursor
	}
}

func readRuns(ctx context.Context, storage *store.Store, workspaceID string, samples *[]time.Duration) ([]domain.RunSummary, map[string]bool, error) {
	values := make([]domain.RunSummary, 0, 8)
	bound := make(map[string]bool, 8)
	cursor := ""
	for {
		page, err := measured(samples, func() (store.RunPage, error) {
			return storage.ListRuns(ctx, store.ListRunsQuery{WorkspaceIdentifier: workspaceID, Cursor: cursor, Limit: store.MaximumReadPageLimit})
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list personal-100 runs: %w", err)
		}
		values = append(values, page.Runs...)
		for id, value := range page.RuntimeHandleBoundIDs {
			bound[id] = value
		}
		if !page.HasMore {
			return values, bound, nil
		}
		cursor = page.NextCursor
	}
}

func writeLogical(digest hash.Hash, fields ...string) {
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(field))
	}
}

func stringsJoined(values []string) string {
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	var buffer bytes.Buffer
	for _, value := range copyOfValues {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		buffer.Write(length[:])
		buffer.WriteString(value)
	}
	return buffer.String()
}
