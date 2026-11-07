package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/kandev/kandev/internal/orchestrator/executor"
	"github.com/kandev/kandev/internal/task/models"
	wfmodels "github.com/kandev/kandev/internal/workflow/models"
	v1 "github.com/kandev/kandev/pkg/api/v1"
)

// nativeExecutorCapture records what the explicit retry actually hands the real
// executor: the composed first prompt and the task state at synchronous start.
type nativeExecutorCapture struct {
	mu           sync.Mutex
	description  string
	stateAtStart v1.TaskState
	started      int
}

func (c *nativeExecutorCapture) snapshot() (string, v1.TaskState, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.description, c.stateAtStart, c.started
}

// A failed root retried through the real RecoverFailedTaskRoot caller reserves
// the root after owned admission, then starts the agent with the workflow
// and task prompt composed exactly once, and restores FAILED when that start
// fails.
func TestExplicitRetrySyncFailureRestoresFailedRootAndSendsWorkflowPrompt(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	const (
		taskID    = "retry-task"
		sessionID = "retry-session"
		stepID    = "retry-step"
	)
	seedTaskAndSessionWithStep(t, repo, taskID, sessionID, stepID)
	task, err := repo.GetTask(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("load seeded task: %v", err)
	}
	task.Description = "Full task instructions for the agent."
	task.State = v1.TaskStateFailed
	if err := repo.UpdateTask(ctx, task); err != nil {
		t.Fatalf("mark task failed: %v", err)
	}
	session, err := repo.GetTaskSession(ctx, sessionID)
	if err != nil || session == nil {
		t.Fatalf("load seeded session: %v", err)
	}
	session.State = models.TaskSessionStateFailed
	session.AgentProfileID = "profile-retry"
	session.ExecutionProfileID = "profile-retry"
	if err := repo.UpdateTaskSession(ctx, session); err != nil {
		t.Fatalf("mark session failed: %v", err)
	}

	steps := newMockStepGetter()
	steps.steps[stepID] = &wfmodels.WorkflowStep{
		ID: stepID, WorkflowID: "wf1", Position: 0, Name: "Implement",
		Prompt: "Approval labels: approve-plan / revise-plan / stop.\n\n{{task_prompt}}",
	}
	capture := &nativeExecutorCapture{}
	startErr := errors.New("native start failed")
	agentMgr := &mockAgentManager{repoForExecutionLookup: repo}
	agentMgr.launchAgentFunc = func(_ context.Context, req *executor.LaunchAgentRequest) (*executor.LaunchAgentResponse, error) {
		capture.mu.Lock()
		capture.description = req.TaskDescription
		capture.mu.Unlock()
		return &executor.LaunchAgentResponse{AgentExecutionID: "execution-retry"}, nil
	}
	agentMgr.startAgentProcessFunc = func(context.Context, string) error {
		current, stateErr := repo.GetTask(ctx, taskID)
		capture.mu.Lock()
		capture.started++
		if stateErr == nil && current != nil {
			capture.stateAtStart = current.State
		}
		capture.mu.Unlock()
		return startErr
	}

	svc := createTestServiceWithAgent(repo, steps, newMockTaskRepo(), agentMgr)
	svc.executor = executor.NewExecutor(agentMgr, repo, testLogger(), executor.ExecutorConfig{})
	svc.taskLaunchRecoveryTasks = &taskLaunchRecoveryServiceFake{repo: repo}

	_, recoverErr := svc.RecoverFailedTaskRoot(ctx, taskID, sessionID, "fresh_start", "stamp-retry")
	if !errors.Is(recoverErr, startErr) {
		t.Fatalf("recover error = %v, want the native start failure", recoverErr)
	}
	description, stateAtStart, started := capture.snapshot()
	if started != 1 {
		t.Fatalf("synchronous start calls = %d, want 1", started)
	}
	if stateAtStart != v1.TaskStateScheduling {
		t.Fatalf("state at synchronous start = %q, want SCHEDULING (reserved before synchronous start)", stateAtStart)
	}
	if !strings.Contains(description, "approve-plan") {
		t.Fatalf("workflow step prompt missing from the first prompt: %q", description)
	}
	if strings.Count(description, "Full task instructions for the agent.") != 1 {
		t.Fatalf("task instructions must appear exactly once: %q", description)
	}
	if strings.Count(description, "approve-plan") != 1 {
		t.Fatalf("step prompt must appear exactly once: %q", description)
	}
	after, err := repo.GetTask(ctx, taskID)
	if err != nil || after == nil {
		t.Fatalf("reload task: %v", err)
	}
	if after.State != v1.TaskStateFailed {
		t.Fatalf("state after the failed retry = %q, want FAILED restored by the owned rollback", after.State)
	}
}

// A competing resume attempt owns the session: the explicit retry must not
// reserve or rewrite the failed root while another owner holds admission.
func TestExplicitRetryRespectsACompetingResumeOwner(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	const (
		taskID    = "retry-owned-task"
		sessionID = "retry-owned-session"
		stepID    = "retry-step"
	)
	seedTaskAndSessionWithStep(t, repo, taskID, sessionID, stepID)
	task, err := repo.GetTask(ctx, taskID)
	if err != nil || task == nil {
		t.Fatalf("load seeded task: %v", err)
	}
	task.State = v1.TaskStateFailed
	if err := repo.UpdateTask(ctx, task); err != nil {
		t.Fatalf("mark task failed: %v", err)
	}
	session, err := repo.GetTaskSession(ctx, sessionID)
	if err != nil || session == nil {
		t.Fatalf("load seeded session: %v", err)
	}
	session.State = models.TaskSessionStateFailed
	session.AgentProfileID = "profile-owned"
	session.ExecutionProfileID = "profile-owned"
	if err := repo.UpdateTaskSession(ctx, session); err != nil {
		t.Fatalf("mark session failed: %v", err)
	}

	agentMgr := &mockAgentManager{repoForExecutionLookup: repo}
	svc := createTestServiceWithAgent(repo, newMockStepGetter(), newMockTaskRepo(), agentMgr)
	svc.executor = executor.NewExecutor(agentMgr, repo, testLogger(), executor.ExecutorConfig{})
	svc.taskLaunchRecoveryTasks = &taskLaunchRecoveryServiceFake{repo: repo}

	owner, owned := svc.resumeAttemptStore().begin(ctx, taskID, sessionID)
	if !owned {
		t.Fatal("the competing owner could not claim the attempt")
	}
	defer owner.finish(svc.resumeAttemptStore())

	_, retryErr := svc.resumeTaskSessionWithContinuationAndReservation(ctx, taskID, sessionID,
		executor.ResumeOptions{ExplicitFailedTaskRetry: true},
		func(context.Context, *resumeAttempt, *executor.TaskExecution) error { return nil }, nil,
	)
	if retryErr == nil {
		t.Fatal("the retry must not proceed while another owner holds the attempt")
	}
	current, err := repo.GetTask(ctx, taskID)
	if err != nil || current == nil {
		t.Fatalf("reload task: %v", err)
	}
	if current.State != v1.TaskStateFailed {
		t.Fatalf("competing owner's root was modified: %q", current.State)
	}
}
