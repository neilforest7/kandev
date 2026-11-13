package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/internal/orchestrator/executor"
	"github.com/kandev/kandev/internal/orchestrator/watcher"
	"github.com/kandev/kandev/internal/task/models"
	sqliterepo "github.com/kandev/kandev/internal/task/repository/sqlite"
	wfmodels "github.com/kandev/kandev/internal/workflow/models"
	"github.com/stretchr/testify/require"
)

// newReadOnlyDenialService mirrors the pilot's read-only review step: it has an
// on_turn_complete transition and does not let a cancel complete the turn.
func newReadOnlyDenialService(t *testing.T) (*Service, *mockAgentManager, *sqliterepo.Repository) {
	t.Helper()
	repo := setupTestRepo(t)
	seedSession(t, repo, "t1", "s1", "step1")
	seedExecutorRunning(t, repo, "s1", "t1", "exec-1")
	setReadOnlyDenialSessionState(t, repo, models.TaskSessionStateRunning)
	stepGetter := newMockStepGetter()
	stepGetter.steps["step1"] = &wfmodels.WorkflowStep{
		ID: "step1", WorkflowID: "wf1", Name: "Read-only Review", Position: 0,
		Events: wfmodels.StepEvents{
			OnTurnComplete: []wfmodels.OnTurnCompleteAction{{Type: wfmodels.OnTurnCompleteMoveToNext}},
		},
	}
	stepGetter.steps["step2"] = &wfmodels.WorkflowStep{ID: "step2", WorkflowID: "wf1", Name: "Next", Position: 1}
	agentMgr := &mockAgentManager{isAgentRunning: true, repoForExecutionLookup: repo}
	svc := createTestServiceWithAgent(repo, stepGetter, newMockTaskRepo(), agentMgr)
	svc.executor = executor.NewExecutor(agentMgr, repo, testLogger(), executor.ExecutorConfig{})
	svc.messageCreator = &mockMessageCreator{}
	return svc, agentMgr, repo
}

func setReadOnlyDenialSessionState(t *testing.T, repo interface {
	GetTaskSession(context.Context, string) (*models.TaskSession, error)
	UpdateTaskSession(context.Context, *models.TaskSession) error
}, state models.TaskSessionState) {
	t.Helper()
	session, err := repo.GetTaskSession(context.Background(), "s1")
	require.NoError(t, err)
	session.State = state
	require.NoError(t, repo.UpdateTaskSession(context.Background(), session))
}

func cancelledReady(readOnlyDenial bool) watcher.AgentEventData {
	return watcher.AgentEventData{
		TaskID: "t1", SessionID: "s1",
		DeliveryOutcome: string(models.DeliverySubmissionCancelled),
		ReadOnlyDenial:  readOnlyDenial,
	}
}

func readOnlyResumePrompts(agentMgr *mockAgentManager) int {
	agentMgr.mu.Lock()
	defer agentMgr.mu.Unlock()
	count := 0
	for _, prompt := range agentMgr.capturedPrompts {
		if strings.Contains(prompt, readOnlyDenialResumePrompt) {
			count++
		}
	}
	return count
}

func requireSessionState(t *testing.T, svc *Service, want models.TaskSessionState) {
	t.Helper()
	require.Eventually(t, func() bool {
		session, err := svc.repo.GetTaskSession(context.Background(), "s1")
		return err == nil && session.State == want
	}, 2*time.Second, 10*time.Millisecond, "session state")
}

func TestReadOnlyDenialResumesTheSessionOnce(t *testing.T) {
	ctx := context.Background()
	svc, agentMgr, _ := newReadOnlyDenialService(t)

	svc.handleAgentReady(ctx, cancelledReady(true))
	require.Eventually(t, func() bool { return readOnlyResumePrompts(agentMgr) == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, 0, svc.messageQueue.GetStatus(ctx, "s1").Count)
	requireSessionState(t, svc, models.TaskSessionStateRunning)
	require.Eventually(t, func() bool { return !svc.isQueuedDispatchInFlight("s1") }, 2*time.Second, 10*time.Millisecond)

	// The resumed turn is aborted by a read-only refusal again.
	svc.handleAgentReady(ctx, cancelledReady(true))
	require.False(t, svc.isQueuedDispatchInFlight("s1"), "the Host must not dispatch a second resume")
	time.Sleep(100 * time.Millisecond) // a wrongly queued resume would reach the agent by now
	require.Equal(t, 1, readOnlyResumePrompts(agentMgr), "the Host must not resume a second time")
	requireSessionState(t, svc, models.TaskSessionStateWaitingForInput)
	require.Equal(t, 0, svc.messageQueue.GetStatus(ctx, "s1").Count)
	task, err := svc.repo.GetTask(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, "step1", task.WorkflowStepID, "an aborted review must not advance the workflow")
}

func TestReadOnlyDenialResumeSkipsOtherTurnEnds(t *testing.T) {
	ctx := context.Background()

	t.Run("user cancel", func(t *testing.T) {
		svc, agentMgr, _ := newReadOnlyDenialService(t)
		svc.handleAgentReady(ctx, cancelledReady(false))
		require.Equal(t, 0, readOnlyResumePrompts(agentMgr))
		require.Equal(t, 0, svc.messageQueue.GetStatus(ctx, "s1").Count)
	})

	t.Run("pending human question", func(t *testing.T) {
		svc, agentMgr, repo := newReadOnlyDenialService(t)
		seedPendingClarificationMessage(t, repo, "t1", "s1")
		svc.handleAgentReady(ctx, cancelledReady(true))
		requireSessionState(t, svc, models.TaskSessionStateWaitingForInput)
		require.Equal(t, 0, readOnlyResumePrompts(agentMgr))
		require.Equal(t, 0, svc.messageQueue.GetStatus(ctx, "s1").Count)
	})
}

// A parked read-only abort has no successor to settle its terminal, so the
// Host must record the no-op workflow outcome itself.
func TestReadOnlyDenialWithoutResumeSettlesTheDelivery(t *testing.T) {
	f := newDeliveryWorkflowOutcomeFixture(t, "review-readonly", "")
	submissionID := "review-cancelled-prompt"
	incarnation := f.session.QueueIncarnationID
	if incarnation == "" {
		incarnation = f.session.ID
	}
	_, err := f.repo.PrepareAgentDeliverySubmission(f.ctx, &models.AgentDeliverySubmission{
		ID: submissionID, SessionID: f.session.ID, IncarnationID: incarnation,
		HarnessGeneration: 1, OwnerGeneration: 1, DispatchAttemptID: "review-cancelled-dispatch",
		PayloadHash: "review-payload", Payload: []byte("prompt"), State: models.DeliverySubmissionDispatching,
	})
	require.NoError(t, err)
	event := persistedTerminalEvent(f.session.ID, incarnation, 1, submissionID, "review-cancelled-stream", "complete")
	event.Payload = []byte(`{"type":"complete","turn_id":"turn-review-cancelled","data":{"stop_reason":"cancelled","read_only_denial":true}}`)
	projectOrchestratorDeliveryEvent(t, f.repo, f.ctx, event)
	f.session.State = models.TaskSessionStateRunning
	require.NoError(t, f.repo.UpdateTaskSession(f.ctx, f.session))
	// The session already used its one automatic resume.
	_, err = f.repo.SetSessionMetadataKeyIfAbsent(f.ctx, f.session.ID, sessionMetaReadOnlyDenialResumed, true)
	require.NoError(t, err)

	f.service.handleAgentReady(f.ctx, watcher.AgentEventData{
		TaskID: f.taskID, SessionID: f.session.ID, TurnID: "turn-review-cancelled",
		ReadOnlyDenial: true, DeliveryOutcome: string(models.DeliverySubmissionCancelled),
		DeliveryStreamID: event.StreamID, DeliveryIncarnationID: incarnation,
		DeliverySubmissionID: submissionID, DeliveryHarnessGeneration: 1, DeliverySequence: 1,
	})

	session, err := f.repo.GetTaskSession(f.ctx, f.session.ID)
	require.NoError(t, err)
	require.Equal(t, models.TaskSessionStateWaitingForInput, session.State)
	submission, err := f.repo.GetAgentDeliverySubmissionMetadata(f.ctx, submissionID)
	require.NoError(t, err)
	require.NotNil(t, submission.FullySettledAt, "the aborted turn's delivery must be fully settled")
}
