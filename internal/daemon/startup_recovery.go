package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"crewfold/internal/recovery"
)

func resolveDaemonDataDir(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", &StartupError{Code: CodeInvalidConfiguration, Message: "--data-dir is required"}
	}
	resolved, err := filepath.Abs(value)
	if err != nil {
		return "", &StartupError{Code: CodeInvalidConfiguration, Message: "resolve data directory", Cause: err}
	}
	resolved = filepath.Clean(resolved)
	selected, err := recovery.ValidateSelectedPath(resolved)
	if err != nil {
		return "", &StartupError{Code: CodeInvalidConfiguration, Message: "data directory is reserved for recovery maintenance", Cause: err}
	}
	return selected, nil
}

// prepareStartupRecovery runs under the already-held data-directory lock and
// before node identity, Store, runtime, provider, worker, or listener setup.
// A pending or interrupted activation remains inert. An activated restore is
// fully reverified and crash-safely consumed before normal startup may proceed.
func prepareStartupRecovery(ctx context.Context, dataDir string) error {
	state, err := recovery.CheckActivationState(dataDir)
	if err != nil {
		return startupRecoveryError("inspect restore activation state", err)
	}
	switch state.Status {
	case recovery.ActivationStateNormal, recovery.ActivationStateConsumed:
		return nil
	case recovery.ActivationStatePending:
		return &StartupError{
			Code:    CodeRestoreNotActivated,
			Message: "restored data directory is pending explicit source-retirement activation",
		}
	case recovery.ActivationStateActivated:
		activated, err := recovery.VerifyActivated(ctx, dataDir)
		if err != nil {
			return startupRecoveryError("verify activated restore before first startup", err)
		}
		if err := recovery.ConsumeActivated(dataDir, activated.ActivationSHA256); err != nil {
			return startupRecoveryError("consume activated restore before first startup", err)
		}
		return nil
	default:
		return &StartupError{
			Code:    CodeRestoreNotActivated,
			Message: fmt.Sprintf("restore activation state is not recognized: %q", state.Status),
		}
	}
}

func startupRecoveryError(operation string, err error) error {
	code := recovery.ErrorCode(err)
	if code == "" {
		code = CodeRestoreNotActivated
	}
	return &StartupError{Code: code, Message: operation, Cause: err}
}
