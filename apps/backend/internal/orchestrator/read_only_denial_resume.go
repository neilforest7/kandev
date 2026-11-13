package orchestrator

import (
	"context"

	"github.com/kandev/kandev/internal/orchestrator/messagequeue"
	"github.com/kandev/kandev/internal/task/models"
	"go.uber.org/zap"
)

// readOnlyDenialResumePrompt is the Host's follow-up after a read-only refusal
// aborted the agent's turn.
const readOnlyDenialResumePrompt = "A write attempt was refused because this session is read-only; " +
	"the turn was interrupted by that refusal, not by a person. " +
	"Do not attempt to write or modify anything again. " +
	"Continue your task and finish with your final result."

// sessionMetaReadOnlyDenialResumed records that the session already received
// its one automatic resume.
const sessionMetaReadOnlyDenialResumed = "read_only_denial_resumed"

// queueMetaReadOnlyDenialResume marks the queued follow-up, and the
// conversation message it becomes, as Host-originated.
const queueMetaReadOnlyDenialResume = "read_only_denial_resume"

// finishReadOnlyDenialTurn settles a turn the read-only policy aborted. It is
// not a person's cancel, so it must neither wait for a cancel owner nor run
// cancellation workflow actions. The first such turn of a session queues one
// Host follow-up so the agent can finish; any later one, or a follow-up that
// cannot be queued, leaves the session waiting for input. Messages already
// queued are delivered first through the ordinary drain.
//
// The caller holds the session's cancelInFlight guard.
func (s *Service) finishReadOnlyDenialTurn(ctx context.Context, taskID, sessionID string, session *models.TaskSession) {
	s.queueReadOnlyDenialResume(ctx, taskID, sessionID)
	if s.drainQueuedBeforeWorkflowTransition(ctx, taskID, sessionID, session) {
		return
	}
	// No successor took over this terminal, so settle it here: the aborted
	// turn has no workflow transition.
	if err := s.settleQueuedDeliveryPredecessor(ctx, taskID, sessionID, workflowEffectFromContext(ctx)); err != nil {
		s.logger.Warn("failed to settle read-only denial terminal",
			zap.String("task_id", taskID), zap.String("session_id", sessionID), zap.Error(err))
	}
	s.setSessionWaitingForInput(ctx, taskID, sessionID, session)
}

func (s *Service) queueReadOnlyDenialResume(ctx context.Context, taskID, sessionID string) {
	if s.messageQueue == nil {
		return
	}
	first, err := s.repo.SetSessionMetadataKeyIfAbsent(ctx, sessionID, sessionMetaReadOnlyDenialResumed, true)
	if err != nil {
		s.logger.Warn("failed to record read-only denial resume; not resuming",
			zap.String("task_id", taskID), zap.String("session_id", sessionID), zap.Error(err))
		return
	}
	if !first {
		s.logger.Info("read-only denial aborted the turn again; leaving the session waiting for input",
			zap.String("task_id", taskID), zap.String("session_id", sessionID))
		return
	}
	if _, err := s.messageQueue.QueueMessageWithMetadata(
		ctx, sessionID, taskID, readOnlyDenialResumePrompt, "",
		messagequeue.QueuedByServer, false, nil,
		map[string]interface{}{queueMetaReadOnlyDenialResume: true},
	); err != nil {
		s.logger.Warn("failed to queue read-only denial resume",
			zap.String("task_id", taskID), zap.String("session_id", sessionID), zap.Error(err))
		return
	}
	s.logger.Info("read-only denial aborted the turn; resuming the session once",
		zap.String("task_id", taskID), zap.String("session_id", sessionID))
	s.publishQueueStatusEvent(ctx, sessionID)
}
