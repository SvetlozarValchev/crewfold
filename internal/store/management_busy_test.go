package store

import "testing"

func TestManagerInvocationTemporarilyBusyIsClosedToExpectedAdmissionFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "live planning run", err: &Error{Code: CodeRunConflict, Message: "planning assignment already has live run run_exact"}, want: true},
		{name: "agent concurrency", err: &Error{Code: CodePlacementUnavailable, Message: "manager agent has reached its exact concurrency limit"}, want: true},
		{name: "node capacity", err: &ExecutionCapacityError{Details: ExecutionCapacityDetails{Dimension: "node_unresolved", Scope: "node", Actual: 20, Limit: 20}}, want: true},
		{name: "stale task", err: &Error{Code: CodeManagerProposalConflict, Message: "planning task is stale"}, want: false},
		{name: "unrelated run conflict", err: &Error{Code: CodeRunConflict, Message: "run is not active"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ManagerInvocationTemporarilyBusy(test.err); got != test.want {
				t.Fatalf("ManagerInvocationTemporarilyBusy(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}
