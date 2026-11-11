package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kandev/kandev/internal/linear"
	"github.com/kandev/kandev/internal/task/models"
	sqliterepo "github.com/kandev/kandev/internal/task/repository/sqlite"
	taskservice "github.com/kandev/kandev/internal/task/service"
)

// repoDescriptionWriter stands in for the task service: it writes the
// description through the repository like UpdateTask does.
type repoDescriptionWriter struct {
	repo  *sqliterepo.Repository
	calls int
}

func (w *repoDescriptionWriter) UpdateTask(ctx context.Context, id string, req *taskservice.UpdateTaskRequest) (*models.Task, error) {
	w.calls++
	task, err := w.repo.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	task.Description = *req.Description
	return task, w.repo.UpdateTask(ctx, task)
}

func seedLinearRoot(t *testing.T, repo *sqliterepo.Repository, description string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateWorkspace(context.Background(), &models.Workspace{ID: "ws-linear", Name: "w", CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, repo.CreateTask(context.Background(), &models.Task{
		ID: "linear-root", WorkspaceID: "ws-linear", Title: "[ENG-1] t", Description: description,
		Metadata: map[string]interface{}{linearWatchMetadataKey: "watch-1"}, CreatedAt: now, UpdatedAt: now,
	}))
}

func TestRefreshLinearRequirementRewritesDescriptionAndDigest(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedLinearRoot(t, repo, "old prompt: first description")
	issue := &linear.LinearIssue{ID: "i1", Identifier: "ENG-1", Title: "t", Description: "second description"}
	fake := &fakeLinearService{snapshot: &linear.TaskIssueSnapshot{Prompt: "prompt: {{issue.description}}", Issue: issue}}
	writer := &repoDescriptionWriter{repo: repo}
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())
	svc.SetLinearService(fake)
	svc.SetLinearRequirementTaskService(writer)

	got, err := svc.refreshLinearRequirementForStart(ctx, "linear-root", "old prompt: first description")
	require.NoError(t, err)
	require.Equal(t, "prompt: second description", got)
	stored, err := repo.GetTask(ctx, "linear-root")
	require.NoError(t, err)
	require.Equal(t, "prompt: second description", stored.Description)
	require.Equal(t, linearRequirementDigest(issue), stored.Metadata[metaKeyLinearRequirementDigest])
	require.Equal(t, "watch-1", stored.Metadata[linearWatchMetadataKey])

	// An up-to-date task is not written again.
	_, err = svc.refreshLinearRequirement(ctx, stored)
	require.NoError(t, err)
	require.Equal(t, 1, writer.calls)
}

func TestRefreshLinearRequirementFailsClosedOnSourceError(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedLinearRoot(t, repo, "old")
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())
	svc.SetLinearService(&fakeLinearService{snapshotErr: errors.New("linear 429")})
	svc.SetLinearRequirementTaskService(&repoDescriptionWriter{repo: repo})
	_, err := svc.refreshLinearRequirementForStart(ctx, "linear-root", "old")
	require.Error(t, err)
}

func TestRefreshLinearRequirementLeavesUnlinkedTasksAlone(t *testing.T) {
	ctx := context.Background()
	repo := setupTestRepo(t)
	seedLinearRoot(t, repo, "old")
	svc := createTestService(repo, newMockStepGetter(), newMockTaskRepo())
	svc.SetLinearService(&fakeLinearService{snapshotErr: linear.ErrSourceLinkNotFound})
	got, err := svc.refreshLinearRequirementForStart(ctx, "linear-root", "old")
	require.NoError(t, err)
	require.Equal(t, "old", got)
}
