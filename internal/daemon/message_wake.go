package daemon

import (
	"context"
	"time"
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
		wakeErr := s.config.MessageWake(wakeContext, job)
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
