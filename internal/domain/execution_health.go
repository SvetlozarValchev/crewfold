package domain

type ExecutionRunCounts struct {
	Unresolved int64 `json:"unresolved"`
	Starting   int64 `json:"starting"`
}

type WorkspaceExecutionHealth struct {
	WorkspaceID string `json:"workspace_id"`
	ExecutionRunCounts
}

type ProjectExecutionHealth struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	ExecutionRunCounts
}

type ProviderExecutionHealth struct {
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider"`
	ExecutionRunCounts
}

type ExecutionQueueState struct {
	Queue           string `json:"queue"`
	Status          string `json:"status"`
	Count           int64  `json:"count"`
	OldestUpdatedAt string `json:"oldest_updated_at,omitempty"`
	OldestAgeMillis int64  `json:"oldest_age_millis"`
}

type TerminalRunLogReferenceHealth struct {
	TerminalRuns          int64 `json:"terminal_runs"`
	StdoutReferences      int64 `json:"stdout_references"`
	StderrReferences      int64 `json:"stderr_references"`
	CompleteStreamPairs   int64 `json:"complete_stream_pairs"`
	RunsWithoutReferences int64 `json:"runs_without_references"`
}

type ExecutionHealth struct {
	ObservedAt  string                        `json:"observed_at"`
	Node        ExecutionRunCounts            `json:"node"`
	Workspaces  []WorkspaceExecutionHealth    `json:"workspaces"`
	Projects    []ProjectExecutionHealth      `json:"projects"`
	Providers   []ProviderExecutionHealth     `json:"providers"`
	Queues      []ExecutionQueueState         `json:"queues"`
	TerminalLog TerminalRunLogReferenceHealth `json:"terminal_log_references"`
}
