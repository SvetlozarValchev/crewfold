package store

import (
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestM21OwnerExecutiveResponseAcceptsBoundedParagraphs(t *testing.T) {
	t.Parallel()

	if !validOwnerExecutiveResponseText("Observed state.\n\nProposed next step.", 8192) {
		t.Fatal("bounded multiline executive response was rejected")
	}
	for _, value := range []string{"", "contains\x00nul", strings.Repeat("x", 8193), string([]byte{0xff})} {
		if validOwnerExecutiveResponseText(value, 8192) {
			t.Fatalf("invalid executive response was accepted: %q", value)
		}
	}
}

func TestM21OwnerExecutiveDecisionRequiresTwoMaterialAlternativesAtStoreBoundary(t *testing.T) {
	t.Parallel()

	choices := []domain.OwnerChoice{
		{Key: "keep-scope", Label: "Keep the accepted scope", Description: "Continue under the current accepted objective."},
		{Key: "change-scope", Label: "Change the accepted scope", Description: "Request a new reviewed objective revision."},
	}
	if !validOwnerExecutiveDecision("Should the accepted project scope change?", "", choices) {
		t.Fatal("exact two-choice executive decision was rejected")
	}
	for _, invalid := range [][]domain.OwnerChoice{nil, {}, choices[:1]} {
		if validOwnerExecutiveDecision("Should the accepted project scope change?", "", invalid) {
			t.Fatalf("executive decision without two alternatives was accepted: %#v", invalid)
		}
	}
	if validOwnerExecutiveDecision("Should the accepted project scope change?", "The executive answered itself.", choices) {
		t.Fatal("executive decision also carried an answer")
	}
}
