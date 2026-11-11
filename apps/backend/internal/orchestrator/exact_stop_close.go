package orchestrator

import (
	"context"
	"fmt"

	"github.com/kandev/kandev/internal/task/models"
)

// CloseStoppedSession records an exact, requested stop of one execution whose
// runtime has confirmed it gone or failed (not found, or a failed execution that
// no longer answers). The session leaves its active state only if it still
// points at that very execution and is still in the state observed now, so a
// replacement execution or a concurrent transition is never closed. It reports
// whether the session was closed.
func (s *Service) CloseStoppedSession(ctx context.Context, sessionID, executionID, reason string) (bool, error) {
	if sessionID == "" || executionID == "" {
		return false, nil
	}
	session, err := s.repo.GetTaskSession(ctx, sessionID)
	if err != nil || session == nil {
		return false, fmt.Errorf("close stopped session: %w", err)
	}
	if session.AgentExecutionID != executionID || !sessionStateActive(session.State) {
		return false, nil
	}
	closed, _, err := s.repo.UpdateTaskSessionStateIfCurrent(ctx, sessionID, session.State, models.TaskSessionStateCancelled, reason)
	if err != nil {
		return false, fmt.Errorf("close stopped session: %w", err)
	}
	return closed, nil
}

func sessionStateActive(state models.TaskSessionState) bool {
	switch state {
	case models.TaskSessionStateCreated, models.TaskSessionStateStarting, models.TaskSessionStateRunning, models.TaskSessionStateWaitingForInput:
		return true
	}
	return false
}
