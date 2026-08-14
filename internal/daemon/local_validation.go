package daemon

import (
	"strings"

	"crewfold/internal/domain"
)

func validCanonicalEntityID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validApprovalListStatus(value string) bool {
	switch value {
	case "", domain.ApprovalPending, domain.ApprovalGranted, domain.ApprovalDenied, domain.ApprovalExpired, domain.ApprovalConsumed:
		return true
	default:
		return false
	}
}

func validCheckListStatus(value string) bool {
	switch value {
	case "", domain.CheckRunRequested, domain.CheckRunStarting, domain.CheckRunRunning, domain.CheckRunFinished:
		return true
	default:
		return false
	}
}

func validCheckListOutcome(value string) bool {
	switch value {
	case "", domain.CheckOutcomePassed, domain.CheckOutcomeFailed, domain.CheckOutcomeTimedOut, domain.CheckOutcomeStartFailed, domain.CheckOutcomeUnknown:
		return true
	default:
		return false
	}
}
