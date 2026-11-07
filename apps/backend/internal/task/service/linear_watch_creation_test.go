package service

import (
	"context"
	"testing"

	"github.com/kandev/kandev/internal/task/models"
)

func TestLinearWatchCreationRecoveryRequiresDurableReceipt(t *testing.T) {
	ctx := context.Background()
	svc, _, repo := createTestService(t)
	wf := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, "watch-receipt-ws")
	id := ReservedExternalIDPrefixLinearWatch + "org:issue"
	request := func() *CreateTaskRequest {
		return &CreateTaskRequest{WorkspaceID: "watch-receipt-ws", WorkflowID: wf, Title: "Issue", ExternalID: id, AllowReservedExternalID: true}
	}
	created, err := svc.CreateTask(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateTaskMetadata(ctx, created.Task.ID, map[string]interface{}{models.MetaKeyLinearWatchCreationComplete: id}); err != nil {
		t.Fatal(err)
	}
	if created.Task.Metadata == nil {
		created.Task.Metadata = make(map[string]interface{})
	}
	created.Task.Metadata[models.MetaKeyLinearWatchCreationComplete] = id
	if err := repo.UpdateTask(ctx, created.Task); err != nil {
		t.Fatal(err)
	}
	if err := svc.SettleLinearWatchCreation(ctx, created.Task.ID, id); err == nil {
		t.Fatal("untrusted metadata must not authorize settlement")
	}
	if err := svc.MarkLinearWatchCreationComplete(ctx, created.Task.ID, id); err != nil {
		t.Fatal(err)
	}
	retry, err := svc.CreateTask(ctx, request())
	if err != nil || retry.Outcome != CreateTaskOutcomeFoundUnsettled || retry.Task.ID != created.Task.ID {
		t.Fatalf("unsettled retry = %+v, %v", retry, err)
	}
	if err := svc.SettleLinearWatchCreation(ctx, retry.Task.ID, id); err != nil {
		t.Fatalf("recover completion: %v", err)
	}
	settled, err := svc.CreateTask(ctx, request())
	if err != nil || settled.Outcome != CreateTaskOutcomeFoundSettled || settled.Task.ID != created.Task.ID {
		t.Fatalf("settled retry = %+v, %v", settled, err)
	}
}
