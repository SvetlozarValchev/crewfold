package domain

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ClaimKindPath      = "path"
	ClaimKindComponent = "component"
	ClaimKindOperation = "operation"

	ClaimModeExclusive = "exclusive"
	ClaimModeShared    = "shared"
	ClaimModeAdvisory  = "advisory"

	ClaimPolicyNotify            = "notify"
	ClaimPolicyDenyNew           = "deny_new"
	ClaimPolicyPauseScheduling   = "pause_scheduling"
	ClaimPolicyRequestResolution = "request_resolution"

	ClaimActive   = "active"
	ClaimExpired  = "expired"
	ClaimReleased = "released"

	OverlapOpen     = "open"
	OverlapResolved = "resolved"

	OverlapSeverityCritical = "critical"
	OverlapSeverityHigh     = "high"
	OverlapSeverityMedium   = "medium"
	OverlapSeverityLow      = "low"

	DriftOpen     = "open"
	DriftResolved = "resolved"
)

type WorkClaim struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	ProjectID      string   `json:"project_id"`
	TaskID         string   `json:"task_id"`
	CheckoutID     string   `json:"checkout_id,omitempty"`
	Kind           string   `json:"kind"`
	Target         string   `json:"target"`
	Mode           string   `json:"mode"`
	ConflictPolicy string   `json:"conflict_policy"`
	Status         string   `json:"status"`
	BaselinePaths  []string `json:"baseline_paths"`
	LeaseExpiresAt string   `json:"lease_expires_at"`
	Revision       int64    `json:"revision"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	CreatedBy      string   `json:"created_by"`
	UpdatedBy      string   `json:"updated_by"`
}

type WorkOverlap struct {
	ID                 string   `json:"id"`
	WorkspaceID        string   `json:"workspace_id"`
	ProjectID          string   `json:"project_id"`
	ClaimIDs           []string `json:"claim_ids"`
	TaskIDs            []string `json:"task_ids"`
	Kind               string   `json:"kind"`
	Witness            string   `json:"witness"`
	Severity           string   `json:"severity"`
	PolicyResponse     string   `json:"policy_response"`
	SchedulingPaused   bool     `json:"scheduling_paused"`
	ResolutionRequired bool     `json:"resolution_required"`
	Status             string   `json:"status"`
	Explanation        []string `json:"explanation"`
	DetectedAt         string   `json:"detected_at"`
	ResolvedAt         string   `json:"resolved_at,omitempty"`
	ResolutionReason   string   `json:"resolution_reason,omitempty"`
	Revision           int64    `json:"revision"`
}

type ClaimDrift struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	ClaimID         string `json:"claim_id"`
	TaskID          string `json:"task_id"`
	CheckoutID      string `json:"checkout_id"`
	Path            string `json:"path"`
	HeadCommit      string `json:"head_commit,omitempty"`
	ObservationGap  bool   `json:"observation_gap"`
	Status          string `json:"status"`
	FirstObservedAt string `json:"first_observed_at"`
	LastObservedAt  string `json:"last_observed_at"`
	ResolvedAt      string `json:"resolved_at,omitempty"`
	Revision        int64  `json:"revision"`
}

type CheckoutClaimScan struct {
	CheckoutID         string   `json:"checkout_id"`
	ProjectID          string   `json:"project_id"`
	HeadCommit         string   `json:"head_commit,omitempty"`
	DirtyPaths         []string `json:"dirty_paths"`
	ObservedAt         string   `json:"observed_at"`
	PreviousObservedAt string   `json:"previous_observed_at,omitempty"`
	ObservationGap     bool     `json:"observation_gap"`
	DriftsOpened       int      `json:"drifts_opened"`
	DriftsResolved     int      `json:"drifts_resolved"`
}

func ValidClaimKind(value string) bool {
	return value == ClaimKindPath || value == ClaimKindComponent || value == ClaimKindOperation
}

func ValidClaimMode(value string) bool {
	return value == ClaimModeExclusive || value == ClaimModeShared || value == ClaimModeAdvisory
}

func ValidClaimPolicy(value string) bool {
	return value == ClaimPolicyNotify || value == ClaimPolicyDenyNew || value == ClaimPolicyPauseScheduling || value == ClaimPolicyRequestResolution
}

func NormalizeClaimTarget(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || value == "" || len(value) > 512 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("claim target must be valid non-control UTF-8 no larger than 512 bytes")
	}
	if kind != ClaimKindPath {
		if kind != ClaimKindComponent && kind != ClaimKindOperation {
			return "", errors.New("claim kind must be path, component, or operation")
		}
		if len(value) > 128 || strings.ContainsAny(value, "\\\n\r\t") {
			return "", errors.New("component and operation targets must contain at most 128 plain characters")
		}
		return value, nil
	}
	if strings.ContainsAny(value, "\\[]{}") || strings.HasPrefix(value, "/") {
		return "", errors.New("path claims are repository-relative and support only literal text, *, ?, and whole-segment **")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", errors.New("path claims cannot contain parent-directory segments")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path claims cannot escape or name only the repository root")
	}
	segments := strings.Split(cleaned, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, "**") && segment != "**" {
			return "", errors.New("** is supported only as a complete path segment")
		}
	}
	return cleaned, nil
}

// ClaimScopesOverlap returns a concrete witness when two declarations may select
// the same repository-relative path or the same named semantic scope.
func ClaimScopesOverlap(kind, left, right string) (string, bool) {
	if kind != ClaimKindPath {
		if (kind == ClaimKindComponent || kind == ClaimKindOperation) && left == right {
			return left, true
		}
		return "", false
	}
	return pathPatternIntersection(left, right)
}

// ClaimPathMatches treats value as a concrete repository-relative path, even
// when a filename itself contains characters such as '*' or '?'.
func ClaimPathMatches(pattern, value string) bool {
	patternParts := strings.Split(pattern, "/")
	valueParts := strings.Split(value, "/")
	type key struct{ pattern, value int }
	memo := make(map[key]bool)
	visited := make(map[key]bool)
	var matches func(int, int) bool
	matches = func(patternIndex, valueIndex int) bool {
		state := key{patternIndex, valueIndex}
		if visited[state] {
			return memo[state]
		}
		visited[state] = true
		matched := false
		switch {
		case patternIndex == len(patternParts):
			matched = valueIndex == len(valueParts)
		case patternParts[patternIndex] == "**":
			matched = matches(patternIndex+1, valueIndex) || valueIndex < len(valueParts) && matches(patternIndex, valueIndex+1)
		case valueIndex < len(valueParts) && segmentPatternMatches(patternParts[patternIndex], valueParts[valueIndex]):
			matched = matches(patternIndex+1, valueIndex+1)
		}
		memo[state] = matched
		return matched
	}
	return value != "" && matches(0, 0)
}

func segmentPatternMatches(pattern, value string) bool {
	p, v := []rune(pattern), []rune(value)
	type key struct{ pattern, value int }
	memo := make(map[key]bool)
	visited := make(map[key]bool)
	var matches func(int, int) bool
	matches = func(patternIndex, valueIndex int) bool {
		state := key{patternIndex, valueIndex}
		if visited[state] {
			return memo[state]
		}
		visited[state] = true
		matched := false
		switch {
		case patternIndex == len(p):
			matched = valueIndex == len(v)
		case p[patternIndex] == '*':
			matched = matches(patternIndex+1, valueIndex) || valueIndex < len(v) && matches(patternIndex, valueIndex+1)
		case valueIndex < len(v) && (p[patternIndex] == '?' || p[patternIndex] == v[valueIndex]):
			matched = matches(patternIndex+1, valueIndex+1)
		}
		memo[state] = matched
		return matched
	}
	return matches(0, 0)
}

func ClaimOverlapSeverity(leftMode, rightMode string) string {
	if leftMode == ClaimModeAdvisory || rightMode == ClaimModeAdvisory {
		return OverlapSeverityLow
	}
	if leftMode == ClaimModeExclusive && rightMode == ClaimModeExclusive {
		return OverlapSeverityCritical
	}
	if leftMode == ClaimModeExclusive || rightMode == ClaimModeExclusive {
		return OverlapSeverityHigh
	}
	return OverlapSeverityMedium
}

func ClaimPolicyResponse(left, right string) string {
	if left == ClaimPolicyDenyNew || right == ClaimPolicyDenyNew {
		return ClaimPolicyDenyNew
	}
	if left == ClaimPolicyPauseScheduling || right == ClaimPolicyPauseScheduling {
		return ClaimPolicyPauseScheduling
	}
	if left == ClaimPolicyRequestResolution || right == ClaimPolicyRequestResolution {
		return ClaimPolicyRequestResolution
	}
	return ClaimPolicyNotify
}

type pathState struct {
	left, right int
	consumed    bool
}

type pathStep struct {
	previous pathState
	segment  string
}

func pathPatternIntersection(left, right string) (string, bool) {
	leftParts, rightParts := strings.Split(left, "/"), strings.Split(right, "/")
	start := pathState{}
	queue := epsilonPathClosure([]pathState{start}, leftParts, rightParts)
	seen := make(map[pathState]bool)
	parents := make(map[pathState]pathStep)
	for len(queue) != 0 {
		state := queue[0]
		queue = queue[1:]
		if seen[state] {
			continue
		}
		seen[state] = true
		if state.left == len(leftParts) && state.right == len(rightParts) && state.consumed {
			segments := make([]string, 0)
			for state != start {
				step, ok := parents[state]
				if !ok {
					break
				}
				if step.segment != "" {
					segments = append(segments, step.segment)
				}
				state = step.previous
			}
			for i, j := 0, len(segments)-1; i < j; i, j = i+1, j-1 {
				segments[i], segments[j] = segments[j], segments[i]
			}
			return strings.Join(segments, "/"), true
		}
		for _, transition := range intersectingPathTransitions(state, leftParts, rightParts) {
			for _, next := range epsilonPathClosure([]pathState{transition.state}, leftParts, rightParts) {
				if _, exists := parents[next]; !exists && next != start {
					parents[next] = pathStep{previous: state, segment: transition.segment}
				}
				if !seen[next] {
					queue = append(queue, next)
				}
			}
		}
	}
	return "", false
}

func epsilonPathClosure(states []pathState, left, right []string) []pathState {
	queue := append([]pathState(nil), states...)
	seen := make(map[pathState]bool)
	result := make([]pathState, 0, len(queue))
	for len(queue) != 0 {
		state := queue[0]
		queue = queue[1:]
		if seen[state] {
			continue
		}
		seen[state] = true
		result = append(result, state)
		if state.left < len(left) && left[state.left] == "**" {
			queue = append(queue, pathState{left: state.left + 1, right: state.right, consumed: state.consumed})
		}
		if state.right < len(right) && right[state.right] == "**" {
			queue = append(queue, pathState{left: state.left, right: state.right + 1, consumed: state.consumed})
		}
	}
	return result
}

type pathTransition struct {
	state   pathState
	segment string
}

func intersectingPathTransitions(state pathState, left, right []string) []pathTransition {
	if state.left >= len(left) || state.right >= len(right) {
		return nil
	}
	leftPattern, rightPattern := left[state.left], right[state.right]
	leftNext, rightNext := state.left+1, state.right+1
	if leftPattern == "**" {
		leftPattern, leftNext = "*", state.left
	}
	if rightPattern == "**" {
		rightPattern, rightNext = "*", state.right
	}
	witness, ok := segmentPatternIntersection(leftPattern, rightPattern)
	if !ok {
		return nil
	}
	return []pathTransition{{state: pathState{left: leftNext, right: rightNext, consumed: true}, segment: witness}}
}

type segmentState struct {
	left, right int
	consumed    bool
}

type segmentStep struct {
	previous segmentState
	value    rune
}

func segmentPatternIntersection(left, right string) (string, bool) {
	l, r := []rune(left), []rune(right)
	start := segmentState{}
	queue := epsilonSegmentClosure([]segmentState{start}, l, r)
	seen := make(map[segmentState]bool)
	parents := make(map[segmentState]segmentStep)
	alphabet := segmentAlphabet(l, r)
	for len(queue) != 0 {
		state := queue[0]
		queue = queue[1:]
		if seen[state] {
			continue
		}
		seen[state] = true
		if state.left == len(l) && state.right == len(r) && state.consumed {
			value := make([]rune, 0)
			for state != start {
				step, ok := parents[state]
				if !ok {
					break
				}
				value = append(value, step.value)
				state = step.previous
			}
			for i, j := 0, len(value)-1; i < j; i, j = i+1, j-1 {
				value[i], value[j] = value[j], value[i]
			}
			return string(value), true
		}
		for _, character := range alphabet {
			leftNext, leftOK := segmentAdvance(l, state.left, character)
			rightNext, rightOK := segmentAdvance(r, state.right, character)
			if !leftOK || !rightOK {
				continue
			}
			for _, next := range epsilonSegmentClosure([]segmentState{{left: leftNext, right: rightNext, consumed: true}}, l, r) {
				if _, exists := parents[next]; !exists && next != start {
					parents[next] = segmentStep{previous: state, value: character}
				}
				if !seen[next] {
					queue = append(queue, next)
				}
			}
		}
	}
	return "", false
}

func epsilonSegmentClosure(states []segmentState, left, right []rune) []segmentState {
	queue := append([]segmentState(nil), states...)
	seen := make(map[segmentState]bool)
	result := make([]segmentState, 0, len(queue))
	for len(queue) != 0 {
		state := queue[0]
		queue = queue[1:]
		if seen[state] {
			continue
		}
		seen[state] = true
		result = append(result, state)
		if state.left < len(left) && left[state.left] == '*' {
			queue = append(queue, segmentState{left: state.left + 1, right: state.right, consumed: state.consumed})
		}
		if state.right < len(right) && right[state.right] == '*' {
			queue = append(queue, segmentState{left: state.left, right: state.right + 1, consumed: state.consumed})
		}
	}
	return result
}

func segmentAdvance(pattern []rune, index int, character rune) (int, bool) {
	if index >= len(pattern) {
		return 0, false
	}
	switch pattern[index] {
	case '*':
		return index, true
	case '?':
		return index + 1, true
	default:
		return index + 1, pattern[index] == character
	}
}

func segmentAlphabet(left, right []rune) []rune {
	values := make(map[rune]bool)
	for _, pattern := range [][]rune{left, right} {
		for _, character := range pattern {
			if character != '*' && character != '?' {
				values[character] = true
			}
		}
	}
	representative := rune('x')
	for values[representative] || representative == '/' {
		representative++
	}
	values[representative] = true
	result := make([]rune, 0, len(values))
	for character := range values {
		result = append(result, character)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func ClaimExplanation(left, right WorkClaim, witness, severity, response string) []string {
	return []string{
		fmt.Sprintf("%s claim %s (%s) intersects %s claim %s (%s)", left.Kind, left.ID, left.Mode, right.Kind, right.ID, right.Mode),
		fmt.Sprintf("concrete shared scope witness: %s", witness),
		fmt.Sprintf("deterministic severity %s from %s/%s mode matrix", severity, left.Mode, right.Mode),
		fmt.Sprintf("policy response %s from %s/%s claim policies", response, left.ConflictPolicy, right.ConflictPolicy),
	}
}
