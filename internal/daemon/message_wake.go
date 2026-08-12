package daemon

import (
	"context"
	"errors"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

const messageWakeTimeout = 5 * time.Second

func (s *server) processMessageWakeJobs() {
	for range 100 {
		job, found, err := s.store.ClaimMessageWakeJob(context.Background(), 5*time.Second)
		if err != nil {
			s.config.Logger.Error("message wake worker could not claim work", "component", "message_wake", "error", err)
			return
		}
		if !found {
			return
		}
		wakeContext, cancel := context.WithTimeout(context.Background(), messageWakeTimeout)
		var wakeErr error
		if s.config.MessageWake != nil {
			wakeErr = s.config.MessageWake(wakeContext, job)
		} else {
			wakeErr = s.wakeMessage(wakeContext, job)
		}
		cancel()
		diagnostic := ""
		if wakeErr != nil {
			diagnostic = wakeErr.Error()
		}
		if err := s.store.CompleteMessageWakeJob(context.Background(), job.ID, wakeErr == nil, diagnostic); err != nil {
			s.config.Logger.Error("message wake worker could not record result", "component", "message_wake", "message_id", job.MessageID, "wake_id", job.ID, "error", err)
			return
		}
	}
}

func (s *server) wakeMessage(ctx context.Context, job domain.MessageWakeJob) error {
	run, err := s.store.RunByID(ctx, job.TargetRunID)
	if err != nil {
		return err
	}
	if run.RuntimeHandle == "" {
		return errors.New("runtime wake-up is unavailable before the target run has a runtime handle")
	}
	driver, exists := s.runtimes[run.Runtime]
	if !exists {
		return errors.New("runtime wake-up is unavailable because the target runtime driver is not registered")
	}
	prompter, supportsPrompt := driver.(execution.RuntimePrompter)
	if !supportsPrompt {
		return errors.New("runtime wake-up is unavailable for this runtime driver")
	}
	return prompter.Prompt(ctx, run.ID, run.RuntimeHandle, "Crewfold mailbox updated. Use crewfold_list_inbox to inspect durable messages.")
}
