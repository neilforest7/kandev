package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kandev/kandev/internal/task/models"
)

// Only the named execution's still-active session is closed.
func TestCloseStoppedSessionOnlyClosesTheNamedExecution(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedTaskAndSession(t, repo, "task", "session", models.TaskSessionStateWaitingForInput)
	require.NoError(t, repo.UpsertExecutorRunning(ctx, &models.ExecutorRunning{
		TaskID: "task", SessionID: "session", AgentExecutionID: "exec-x", Status: models.ExecutorRunningStatusRunning,
	}))
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())

	closed, err := svc.CloseStoppedSession(ctx, "session", "exec-other", "reason")
	require.NoError(t, err)
	require.False(t, closed, "a different execution is never closed")
	session, err := repo.GetTaskSession(ctx, "session")
	require.NoError(t, err)
	require.Equal(t, models.TaskSessionStateWaitingForInput, session.State)

	closed, err = svc.CloseStoppedSession(ctx, "session", "exec-x", "reason")
	require.NoError(t, err)
	require.True(t, closed)
	session, err = repo.GetTaskSession(ctx, "session")
	require.NoError(t, err)
	require.Equal(t, models.TaskSessionStateCancelled, session.State)

	closed, err = svc.CloseStoppedSession(ctx, "session", "exec-x", "reason")
	require.NoError(t, err)
	require.False(t, closed, "an already inactive session is left alone")
}
