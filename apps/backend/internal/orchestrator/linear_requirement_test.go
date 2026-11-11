package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kandev/kandev/internal/linear"
	"github.com/kandev/kandev/internal/orchestrator/executor"
	"github.com/kandev/kandev/internal/task/models"
	sqliterepo "github.com/kandev/kandev/internal/task/repository/sqlite"
	taskservice "github.com/kandev/kandev/internal/task/service"
	v1 "github.com/kandev/kandev/pkg/api/v1"
)

// repoDescriptionWriter stands in for the task service: it writes the
// description through the repository like UpdateTask does.
type repoDescriptionWriter struct {
	repo  *sqliterepo.Repository
	calls int
	// onWrite runs after the description is stored, before UpdateTask returns.
	onWrite func()
}

func (w *repoDescriptionWriter) UpdateTask(ctx context.Context, id string, req *taskservice.UpdateTaskRequest) (*models.Task, error) {
	w.calls++
	task, err := w.repo.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	task.Description = *req.Description
	if err := w.repo.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	if w.onWrite != nil {
		w.onWrite()
	}
	return task, nil
}

const linearTemplate = "prompt: {{issue.description}}"

func linearSnapshot(description string) *linear.TaskIssueSnapshot {
	return &linear.TaskIssueSnapshot{Prompt: linearTemplate, Issue: &linear.LinearIssue{ID: "i1", Identifier: "ENG-1", Title: "t", Description: description}}
}

// linearize turns an existing seeded task into a Linear watch root whose stored
// description is stale against the fake issue.
func linearize(t *testing.T, repo *sqliterepo.Repository, svc *Service, taskID string, fake *fakeLinearService) {
	t.Helper()
	ctx := context.Background()
	task, err := repo.GetTask(ctx, taskID)
	require.NoError(t, err)
	task.Description = "prompt: first description"
	require.NoError(t, repo.UpdateTask(ctx, task))
	require.NoError(t, repo.SetTaskMetadataKey(ctx, taskID, linearWatchMetadataKey, "watch-1"))
	svc.SetLinearService(fake)
	svc.SetLinearRequirementTaskService(&repoDescriptionWriter{repo: repo})
}

func sessionDigest(t *testing.T, repo *sqliterepo.Repository, sessionID string) any {
	t.Helper()
	session, err := repo.GetTaskSession(context.Background(), sessionID)
	require.NoError(t, err)
	return session.Metadata[sessionMetaKeyLinearRequirementDigest]
}

// The vectors are shared with the bridge test (test/projection.test.ts).
func TestLinearRequirementDigestVectors(t *testing.T) {
	for _, vector := range []struct{ title, description, digest string }{
		{"Use A & B", "Render <input>", "b64757c20f6a2ec785564c3e9845dffe58bb34e52deb9aba86ebfa935135b78b"},
		{"T", "line sep ", "86337fdedf6b904d4550bad44bd52850915dcc1d0ef9cd0a04cbf0da599f74fd"},
		{"emoji 🚀", "done ✓", "96e015b7a0914b61acd9d7d99984015725c7700b0b8064207bbe1675b08ab595"},
		{"", "", "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d"},
	} {
		require.Equal(t, vector.digest, linearRequirementDigest(&linear.LinearIssue{Title: vector.title, Description: vector.description}), "%q %q", vector.title, vector.description)
	}
}

func TestRefreshLinearRequirementFailsClosedOnSourceError(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedTaskAndSession(t, repo, "linear-root", "s1", models.TaskSessionStateCreated)
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())
	linearize(t, repo, svc, "linear-root", &fakeLinearService{snapshotErr: errors.New("linear 429")})
	_, _, err := svc.refreshLinearRequirementForStart(ctx, "linear-root", "prompt: first description")
	require.Error(t, err)
}

func TestRefreshLinearRequirementLeavesUnlinkedTasksAlone(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedTaskAndSession(t, repo, "linear-root", "s1", models.TaskSessionStateCreated)
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())
	linearize(t, repo, svc, "linear-root", &fakeLinearService{snapshotErr: linear.ErrSourceLinkNotFound})
	_, got, err := svc.refreshLinearRequirementForStart(ctx, "linear-root", "keep")
	require.NoError(t, err)
	require.Equal(t, "keep", got)
}

// The first requirement a session is sent sticks; a different one later makes
// the session conflicting rather than silently rebinding it.
func TestStampLinearRequirementIsImmutable(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedTaskAndSession(t, repo, "t", "s1", models.TaskSessionStateCreated)
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())
	require.NoError(t, svc.stampLinearRequirement(ctx, "s1", "aaa"))
	require.NoError(t, svc.stampLinearRequirement(ctx, "s1", "aaa"))
	require.Equal(t, "aaa", sessionDigest(t, repo, "s1"))
	require.NoError(t, svc.stampLinearRequirement(ctx, "s1", "bbb"))
	require.Equal(t, linearRequirementConflict, sessionDigest(t, repo, "s1"))
	require.NoError(t, svc.stampLinearRequirement(ctx, "s1", "aaa"))
	require.Equal(t, linearRequirementConflict, sessionDigest(t, repo, "s1"), "reverting content must not clear a conflict")
}

// startTask: the dispatched session is stamped with the digest of the text it
// was sent; a refused launch stamps nothing; a failed source read dispatches nothing.
func TestStartTaskDispatchesRefreshedRequirementAndStampsSession(t *testing.T) {
	ctx := context.Background()
	setup := func(fake *fakeLinearService) (*sqliterepo.Repository, *Service, *int) {
		repo := setupTestRepo(t)
		seedTaskAndSession(t, repo, "task-linear", "existing", models.TaskSessionStateCompleted)
		taskRepo := newMockTaskRepo()
		taskRepo.tasks["task-linear"] = &v1.Task{ID: "task-linear", Title: "t", State: v1.TaskStateInProgress}
		launches := 0
		agentMgr := &mockAgentManager{launchAgentFunc: func(context.Context, *executor.LaunchAgentRequest) (*executor.LaunchAgentResponse, error) {
			launches++
			return &executor.LaunchAgentResponse{AgentExecutionID: "exec-1"}, nil
		}}
		svc := createTestServiceWithScheduler(repo, newMockStepGetter(), taskRepo, agentMgr)
		linearize(t, repo, svc, "task-linear", fake)
		return repo, svc, &launches
	}
	issue := linearSnapshot("second description")
	want := linearRequirementDigest(issue.Issue)

	repo, svc, launches := setup(&fakeLinearService{snapshot: issue})
	execution, err := svc.StartTask(ctx, "task-linear", "profile-1", "", "", "", "prompt: first description", "", false, false, nil)
	require.NoError(t, err)
	require.Equal(t, 1, *launches)
	task, err := repo.GetTask(ctx, "task-linear")
	require.NoError(t, err)
	require.Equal(t, "prompt: second description", task.Description)
	require.Equal(t, want, sessionDigest(t, repo, execution.SessionID))

	// Refused before launch (no profile): the description may refresh, no session is stamped.
	repo, svc, launches = setup(&fakeLinearService{snapshot: issue})
	_, err = svc.StartTask(ctx, "task-linear", "", "", "", "", "prompt: first description", "", false, false, nil)
	require.Error(t, err)
	require.Zero(t, *launches)
	sessions, listErr := repo.ListTaskSessions(ctx, "task-linear")
	require.NoError(t, listErr)
	for _, session := range sessions {
		require.Nil(t, session.Metadata[sessionMetaKeyLinearRequirementDigest])
	}

	// Source read failure: nothing is dispatched.
	_, svc, launches = setup(&fakeLinearService{snapshotErr: errors.New("linear down")})
	_, err = svc.StartTask(ctx, "task-linear", "profile-1", "", "", "", "prompt: first description", "", false, false, nil)
	require.Error(t, err)
	require.Zero(t, *launches)
}

// Native workflow entry (StartSessionForWorkflowStep) and the busy queued entry.
func TestWorkflowEntryAndQueuedEntryDispatchRefreshedRequirement(t *testing.T) {
	ctx := context.Background()
	issue := linearSnapshot("second description")
	want := linearRequirementDigest(issue.Issue)

	t.Run("native entry", func(t *testing.T) {
		repo, svc, manager := setupBusyParentChildrenCompleted(t)
		svc.workflowStepGetter.(*mockStepGetter).steps["step_coordinate"].Prompt = "{{task_prompt}}"
		linearize(t, repo, svc, busyParentID, &fakeLinearService{snapshot: issue})
		require.NoError(t, repo.UpdateTaskSessionState(ctx, busyParentSessionID, models.TaskSessionStateWaitingForInput, ""))
		require.NoError(t, svc.StartSessionForWorkflowStep(ctx, busyParentID, busyParentSessionID, "step_coordinate"))
		require.Len(t, manager.capturedPrompts, 1)
		require.Contains(t, manager.capturedPrompts[0], "prompt: second description")
		require.NotContains(t, manager.capturedPrompts[0], "first description")
		require.Equal(t, want, sessionDigest(t, repo, busyParentSessionID))
	})
	t.Run("native entry source failure", func(t *testing.T) {
		repo, svc, manager := setupBusyParentChildrenCompleted(t)
		linearize(t, repo, svc, busyParentID, &fakeLinearService{snapshotErr: errors.New("linear down")})
		require.NoError(t, repo.UpdateTaskSessionState(ctx, busyParentSessionID, models.TaskSessionStateWaitingForInput, ""))
		require.Error(t, svc.StartSessionForWorkflowStep(ctx, busyParentID, busyParentSessionID, "step_coordinate"))
		require.Empty(t, manager.capturedPrompts)
		require.Nil(t, sessionDigest(t, repo, busyParentSessionID))
	})
	t.Run("busy queued entry", func(t *testing.T) {
		repo, svc, _ := setupBusyParentChildrenCompleted(t)
		svc.workflowStepGetter.(*mockStepGetter).steps["step_coordinate"].Prompt = "{{task_prompt}}"
		linearize(t, repo, svc, busyParentID, &fakeLinearService{snapshot: issue})
		require.NoError(t, svc.stampLinearRequirement(ctx, busyParentSessionID, "digest-of-an-earlier-turn"))
		require.NoError(t, repo.UpdateTaskSessionState(ctx, busyParentSessionID, models.TaskSessionStateRunning, ""))
		cb := &autoStartAgentCallback{svc: svc}
		queued, err := cb.queueStepPromptIfBusy(ctx, busyParentAutoStartInput(), nil)
		require.NoError(t, err)
		require.True(t, queued)
		entries, _, err := svc.messageQueue.SnapshotSession(ctx, busyParentSessionID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.True(t, strings.Contains(entries[0].Content, "prompt: second description"), entries[0].Content)
		// The session already saw another requirement, so it is marked conflicting.
		require.Equal(t, linearRequirementConflict, sessionDigest(t, repo, busyParentSessionID))
	})
	t.Run("busy queued entry source failure", func(t *testing.T) {
		repo, svc, _ := setupBusyParentChildrenCompleted(t)
		linearize(t, repo, svc, busyParentID, &fakeLinearService{snapshotErr: errors.New("linear down")})
		require.NoError(t, repo.UpdateTaskSessionState(ctx, busyParentSessionID, models.TaskSessionStateRunning, ""))
		cb := &autoStartAgentCallback{svc: svc}
		_, err := cb.queueStepPromptIfBusy(ctx, busyParentAutoStartInput(), nil)
		require.Error(t, err)
		entries, _, snapErr := svc.messageQueue.SnapshotSession(ctx, busyParentSessionID)
		require.NoError(t, snapErr)
		require.Empty(t, entries)
	})
}

// Two refreshes interleave: the second one runs completely (and rewrites the
// stored description) between the first one's write and its return. Each result
// must still pair its own text with its own digest; reading the description back
// after the write would hand the first caller the second one's text.
func TestRefreshLinearRequirementPairsTextAndDigestUnderInterleaving(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedTaskAndSession(t, repo, "linear-root", "s1", models.TaskSessionStateCreated)
	snapshotA, snapshotB := linearSnapshot("description A"), linearSnapshot("description B")
	fake := &fakeLinearService{snapshot: snapshotA}
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())
	linearize(t, repo, svc, "linear-root", fake)
	writer := &repoDescriptionWriter{repo: repo}
	svc.SetLinearRequirementTaskService(writer)

	var nested *linearRequirement
	writer.onWrite = func() {
		writer.onWrite = nil
		fake.snapshot = snapshotB
		task, err := repo.GetTask(ctx, "linear-root")
		require.NoError(t, err)
		_, nested, err = svc.refreshLinearRequirement(ctx, task)
		require.NoError(t, err)
	}
	task, err := repo.GetTask(ctx, "linear-root")
	require.NoError(t, err)
	got, req, err := svc.refreshLinearRequirement(ctx, task)
	require.NoError(t, err)

	require.Equal(t, "prompt: description A", req.Description)
	require.Equal(t, linearRequirementDigest(snapshotA.Issue), req.Digest)
	require.Equal(t, req.Description, got.Description, "the task handed to prompt composition carries the same snapshot")
	require.Equal(t, "prompt: description B", nested.Description)
	require.Equal(t, linearRequirementDigest(snapshotB.Issue), nested.Digest)
}
