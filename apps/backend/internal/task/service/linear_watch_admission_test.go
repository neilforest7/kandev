package service

import (
	"context"
	"errors"
	"testing"

	"github.com/kandev/kandev/internal/task/models"
)

func TestLinearWatchAdmissionDeniesParentAndChild(t *testing.T) {
	tasks := map[string]*models.Task{
		"root":  {ID: "root", ExternalID: ReservedExternalIDPrefixLinearWatch + "org:issue"},
		"child": {ID: "child", ParentID: "root"},
		"other": {ID: "other"},
	}
	get := func(_ context.Context, id string) (*models.Task, error) { return tasks[id], nil }
	for _, id := range []string{"root", "child"} {
		if err := denyUnapprovedLinearWatch(context.Background(), id, get); !errors.Is(err, errLinearWatchApprovalRequired) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if err := denyUnapprovedLinearWatch(context.Background(), "other", get); err != nil {
		t.Fatal(err)
	}
	if err := denyUnapprovedLinearWatch(context.Background(), "missing", get); err == nil {
		t.Fatal("missing task must fail closed")
	}
}

func TestLinearWatchIdentityRecoversArchivedTask(t *testing.T) {
	svc, _, repo := createTestService(t)
	ctx := context.Background()
	wf := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, "linear-watch-ws")
	identity := ReservedExternalIDPrefixLinearWatch + "org:issue"
	request := func() *CreateTaskRequest {
		return &CreateTaskRequest{WorkspaceID: "linear-watch-ws", WorkflowID: wf, Title: "Issue", ExternalID: identity, AllowReservedExternalID: true}
	}
	first, err := svc.CreateTask(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateTask(ctx, &CreateTaskRequest{WorkspaceID: "linear-watch-ws", WorkflowID: wf, Title: "Child", ParentID: first.Task.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []*models.Task{first.Task, child.Task} {
		sessionID := "session-" + task.ID
		if err := repo.CreateTaskSession(ctx, &models.TaskSession{ID: sessionID, TaskID: task.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.PrepareExecutorAdmission(ctx, sessionID, "", ""); !errors.Is(err, errLinearWatchApprovalRequired) {
			t.Fatalf("executor admission for %s: %v", task.ID, err)
		}
	}
	unsettled, err := svc.CreateTask(ctx, request())
	if err != nil || unsettled.Outcome != CreateTaskOutcomeFoundUnsettled || unsettled.Task.ID != first.Task.ID {
		t.Fatalf("unsettled retry = %+v, %v", unsettled, err)
	}
	mustSettle(t, ctx, repo, first.Task.ID, identity)
	if err := repo.ArchiveTask(ctx, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := svc.CreateTask(ctx, request())
	if err != nil || retry.Outcome != CreateTaskOutcomeFoundSettled || retry.Task.ID != first.Task.ID {
		t.Fatalf("archived retry = %+v, %v", retry, err)
	}
	if _, err := svc.ReleaseTaskExternalID(ctx, "linear-watch-ws", identity); err == nil {
		t.Fatal("reserved identity was released")
	}
}

func TestLinearWatchProvenanceSurvivesMutation(t *testing.T) {
	svc, _, repo := createTestService(t)
	ctx := context.Background()
	wf := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, "legacy-watch-ws")
	root, err := svc.CreateTask(ctx, &CreateTaskRequest{WorkspaceID: "legacy-watch-ws", WorkflowID: wf, Title: "Legacy", Metadata: map[string]interface{}{models.MetaKeyLinearIssueWatchID: "watch"}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateTask(ctx, &CreateTaskRequest{WorkspaceID: "legacy-watch-ws", WorkflowID: wf, Title: "Child", ParentID: root.Task.ID})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateTask(ctx, root.Task.ID, &UpdateTaskRequest{Metadata: map[string]interface{}{}})
	if err != nil || updated.Metadata[models.MetaKeyLinearIssueWatchID] != "watch" {
		t.Fatalf("legacy provenance: %v, %v", updated, err)
	}
	updated.Metadata = map[string]interface{}{}
	if err := repo.UpdateTask(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateTaskMetadata(ctx, root.Task.ID, map[string]interface{}{models.MetaKeyLinearIssueWatchID: ""}); err != nil {
		t.Fatal(err)
	}
	if err := denyUnapprovedLinearWatch(ctx, root.Task.ID, repo.GetTask); !errors.Is(err, errLinearWatchApprovalRequired) {
		t.Fatalf("legacy admission: %v", err)
	}
	clear := ""
	if _, err := svc.UpdateTask(ctx, child.Task.ID, &UpdateTaskRequest{ParentID: &clear}); !errors.Is(err, errLinearWatchApprovalRequired) {
		t.Fatalf("reparent: %v", err)
	}
	if _, err := svc.DetachTask(ctx, child.Task.ID); !errors.Is(err, errLinearWatchApprovalRequired) {
		t.Fatalf("detach: %v", err)
	}
	if err := repo.ReparentDirectChildrenInWorkspace(ctx, root.Task.ID, "", "legacy-watch-ws"); !errors.Is(err, errLinearWatchApprovalRequired) {
		t.Fatalf("bulk detach: %v", err)
	}
}
