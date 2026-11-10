package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kandev/kandev/internal/orchestrator/executor"
	"github.com/kandev/kandev/internal/task/models"
	wfmodels "github.com/kandev/kandev/internal/workflow/models"
	v1 "github.com/kandev/kandev/pkg/api/v1"
)

// A launch that fails before the executor persists a typed error (JUP-58: the
// launch context was canceled during environment lookup) must still leave a
// stamped retry_launch error, so the explicit retry can reserve the FAILED
// root instead of leaving it with no recovery entry.
func TestEarlyLaunchFailureLeavesRetryLaunchForFailedRoot(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	const (
		taskID    = "early-fail-task"
		sessionID = "early-fail-session"
		stepID    = "early-fail-step"
	)
	seedTaskAndSessionWithStep(t, repo, taskID, sessionID, stepID)
	session, err := repo.GetTaskSession(ctx, sessionID)
	if err != nil || session == nil {
		t.Fatalf("load seeded session: %v", err)
	}
	session.AgentProfileID = "profile-early"
	session.ExecutionProfileID = "profile-early"
	if err := repo.UpdateTaskSession(ctx, session); err != nil {
		t.Fatalf("set session profile: %v", err)
	}

	steps := newMockStepGetter()
	steps.steps[stepID] = &wfmodels.WorkflowStep{ID: stepID, WorkflowID: "wf1", Name: "Investigate"}
	startErr := errors.New("native start failed")
	var stateAtStart v1.TaskState
	agentMgr := &mockAgentManager{repoForExecutionLookup: repo}
	agentMgr.launchAgentFunc = func(context.Context, *executor.LaunchAgentRequest) (*executor.LaunchAgentResponse, error) {
		return &executor.LaunchAgentResponse{AgentExecutionID: "execution-early"}, nil
	}
	agentMgr.startAgentProcessFunc = func(context.Context, string) error {
		if current, _ := repo.GetTask(ctx, taskID); current != nil {
			stateAtStart = current.State
		}
		return startErr
	}
	svc := createTestServiceWithAgent(repo, steps, newMockTaskRepo(), agentMgr)
	svc.executor = executor.NewExecutor(agentMgr, repo, testLogger(), executor.ExecutorConfig{})
	svc.taskLaunchRecoveryTasks = &taskLaunchRecoveryServiceFake{repo: repo}
	svc.taskLaunchRecoveryRepo = repo

	launchErr := fmt.Errorf("lookup existing task environment: %w", context.Canceled)
	_ = svc.handleSessionLaunchFailure(ctx, taskID, sessionID, launchErr)

	failed, err := repo.GetTaskSession(ctx, sessionID)
	if err != nil || failed == nil || failed.State != models.TaskSessionStateFailed {
		t.Fatalf("session after early failure = %+v, %v; want FAILED", failed, err)
	}
	lastErr, found := models.LoadLastAgentError(failed.Metadata)
	if !found || lastErr.IsDismissed() {
		t.Fatal("early launch failure left no active launch error to recover from")
	}
	// Production fails the root through the primary-session task update.
	task, err := repo.GetTask(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("load task: %v", err)
	}
	task.State = v1.TaskStateFailed
	if err := repo.UpdateTask(ctx, task); err != nil {
		t.Fatalf("mark root failed: %v", err)
	}

	_, recoverErr := svc.RecoverTaskLaunch(ctx, &TaskLaunchRecoveryRequest{
		TaskID: taskID, SessionID: sessionID, Action: models.RecoveryActionRetryLaunch, ErrorStamp: lastErr.Stamp(),
	})
	if !errors.Is(recoverErr, startErr) {
		t.Fatalf("retry_launch error = %v, want it to reach the agent start", recoverErr)
	}
	if stateAtStart != v1.TaskStateScheduling {
		t.Fatalf("root state at retried start = %q, want SCHEDULING (failed root reserved)", stateAtStart)
	}
}
