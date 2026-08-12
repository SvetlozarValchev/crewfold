package domain

import "testing"

func TestNormalizeClaimTarget(t *testing.T) {
	t.Parallel()
	for input, wanted := range map[string]string{
		"./src/contact/**":  "src/contact/**",
		"src//contact/*.go": "src/contact/*.go",
		"docs/?eadme.md":    "docs/?eadme.md",
	} {
		got, err := NormalizeClaimTarget(ClaimKindPath, input)
		if err != nil || got != wanted {
			t.Errorf("NormalizeClaimTarget(%q) = %q, %v; want %q", input, got, err, wanted)
		}
	}
	for _, invalid := range []string{"", "/etc/**", "../other/**", "src/../docs/**", "src/**thing", "src/[ab].go", "**\\secret"} {
		if _, err := NormalizeClaimTarget(ClaimKindPath, invalid); err == nil {
			t.Errorf("NormalizeClaimTarget(%q) error = nil", invalid)
		}
	}
}

func TestClaimPathIntersectionReturnsConcreteWitness(t *testing.T) {
	t.Parallel()
	cases := []struct {
		left, right string
		overlap     bool
	}{
		{"src/contact/**", "src/contact/cache.go", true},
		{"src/*.go", "src/contact.go", true},
		{"**/cache?.go", "src/cache1.go", true},
		{"src/*/test.go", "src/**", true},
		{"src/contact/**", "docs/**", false},
		{"src/*.go", "src/*.md", false},
		{"cmd/?/main.go", "cmd/long/main.go", false},
	}
	for _, test := range cases {
		witness, overlap := ClaimScopesOverlap(ClaimKindPath, test.left, test.right)
		if overlap != test.overlap || overlap && witness == "" {
			t.Errorf("ClaimScopesOverlap(%q, %q) = %q, %t; want overlap=%t", test.left, test.right, witness, overlap, test.overlap)
		}
	}
}

func TestClaimConflictMatrixIsDeterministic(t *testing.T) {
	t.Parallel()
	if got := ClaimOverlapSeverity(ClaimModeExclusive, ClaimModeExclusive); got != OverlapSeverityCritical {
		t.Fatalf("exclusive/exclusive severity = %q", got)
	}
	if got := ClaimOverlapSeverity(ClaimModeExclusive, ClaimModeShared); got != OverlapSeverityHigh {
		t.Fatalf("exclusive/shared severity = %q", got)
	}
	if got := ClaimOverlapSeverity(ClaimModeShared, ClaimModeShared); got != OverlapSeverityMedium {
		t.Fatalf("shared/shared severity = %q", got)
	}
	if got := ClaimOverlapSeverity(ClaimModeExclusive, ClaimModeAdvisory); got != OverlapSeverityLow {
		t.Fatalf("advisory severity = %q", got)
	}
	if got := ClaimPolicyResponse(ClaimPolicyNotify, ClaimPolicyDenyNew); got != ClaimPolicyDenyNew {
		t.Fatalf("deny response = %q", got)
	}
	if got := ClaimPolicyResponse(ClaimPolicyRequestResolution, ClaimPolicyPauseScheduling); got != ClaimPolicyPauseScheduling {
		t.Fatalf("pause response = %q", got)
	}
}

func TestClaimPathMatchesTreatsObservedPathAsConcrete(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		pattern, value string
		matches        bool
	}{
		{"src/**", "src/contact/cache.go", true},
		{"src/*.go", "src/contact/cache.go", false},
		{"src/*.go", "src/literal*.go", true},
		{"src/file?.go", "src/file?.go", true},
		{"src/?", "src/long", false},
		{"**/cache.go", "cache.go", true},
	} {
		if got := ClaimPathMatches(test.pattern, test.value); got != test.matches {
			t.Errorf("ClaimPathMatches(%q, %q) = %t, want %t", test.pattern, test.value, got, test.matches)
		}
	}
}

func TestClaimPathIntersectionAgreesWithBoundedEnumeration(t *testing.T) {
	t.Parallel()
	patterns := []string{"a", "b", "*", "?", "**", "a/*", "*/b", "**/a", "a/**", "a/?", "?/**"}
	paths := []string{"a", "b", "x", "aa"}
	for depth := 2; depth <= 4; depth++ {
		paths = append(paths, enumerateClaimTestPaths([]string{"a", "b", "x", "aa"}, depth)...)
	}
	for _, left := range patterns {
		for _, right := range patterns {
			expected := false
			for _, candidate := range paths {
				if ClaimPathMatches(left, candidate) && ClaimPathMatches(right, candidate) {
					expected = true
					break
				}
			}
			witness, got := ClaimScopesOverlap(ClaimKindPath, left, right)
			if got != expected {
				t.Errorf("ClaimScopesOverlap(%q, %q) = %q, %t; enumerated=%t", left, right, witness, got, expected)
				continue
			}
			if got && (!ClaimPathMatches(left, witness) || !ClaimPathMatches(right, witness)) {
				t.Errorf("intersection witness %q does not match both %q and %q", witness, left, right)
			}
		}
	}
}

func enumerateClaimTestPaths(segments []string, depth int) []string {
	if depth == 1 {
		return append([]string(nil), segments...)
	}
	tails := enumerateClaimTestPaths(segments, depth-1)
	result := make([]string, 0, len(segments)*len(tails))
	for _, segment := range segments {
		for _, tail := range tails {
			result = append(result, segment+"/"+tail)
		}
	}
	return result
}
