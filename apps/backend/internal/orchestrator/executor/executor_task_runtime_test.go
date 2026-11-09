package executor

import (
	"context"
	"testing"

	agentruntime "github.com/kandev/kandev/internal/agentruntime"
	"github.com/kandev/kandev/internal/task/models"
)

// A watch root exists before any worktree or workspace default executor does.
// The task's executor profile metadata is the native target the launch path
// uses, so the roster runtime must come from it and not from a guess.
func TestResolveTaskRuntimeUsesTheTaskExecutorProfile(t *testing.T) {
	repo := newMockRepository()
	repo.tasks["watch-root"] = &models.Task{
		ID:          "watch-root",
		WorkspaceID: "workspace-1",
		Metadata:    map[string]interface{}{models.MetaKeyExecutorProfileID: "profile-worktree"},
	}
	repo.executorProfiles["profile-worktree"] = &models.ExecutorProfile{ID: "profile-worktree", ExecutorID: "executor-worktree"}
	repo.executors["executor-worktree"] = &models.Executor{ID: "executor-worktree", Type: models.ExecutorTypeLocalDocker}

	exec := newTestExecutor(t, nil, repo)
	got, err := exec.ResolveTaskRuntime(context.Background(), "watch-root")
	if err != nil {
		t.Fatalf("resolve task runtime: %v", err)
	}
	want := string(models.ExecutorTypeLocalDocker.Runtime())
	if want == "" || got != want {
		t.Fatalf("runtime = %q, want the task executor profile runtime %q", got, want)
	}
	if got == string(agentruntime.RuntimeStandalone) {
		t.Fatalf("runtime must come from the task executor profile, not a default guess, got %q", got)
	}
}

func TestResolveTaskRuntimeFailsClosedWithoutATarget(t *testing.T) {
	repo := newMockRepository()
	repo.tasks["bare"] = &models.Task{ID: "bare"}
	exec := newTestExecutor(t, nil, repo)
	if _, err := exec.ResolveTaskRuntime(context.Background(), "bare"); err == nil {
		t.Fatal("an unresolved executor target must not produce a runtime")
	}
	if _, err := exec.ResolveTaskRuntime(context.Background(), ""); err == nil {
		t.Fatal("a missing task must not produce a runtime")
	}
}
