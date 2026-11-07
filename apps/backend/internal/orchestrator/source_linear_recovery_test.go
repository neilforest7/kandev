package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/kandev/kandev/internal/linear"
)

func TestLinearAttachFailureDoesNotStartTask(t *testing.T) {
	linearService := &fakeLinearService{reserveOK: true, assignErr: errors.New("attach failed")}
	source := NewLinearWatcherSource(linearService, nopLogger(t))
	starter := &fakeTaskStarter{}
	coordinator := newTestCoordinator(t, &fakeTaskCreator{}, true, starter)
	coordinator.Dispatch(context.Background(), source, sampleLinearEvent())
	if starter.called != 0 {
		t.Fatalf("started task without source association: %d", starter.called)
	}
	if len(linearService.gotRelease) != 0 {
		t.Fatal("released reservation after task creation")
	}
}

func TestLinearWatchDoesNotAutoStartBeforeApproval(t *testing.T) {
	linearService := &fakeLinearService{reserveOK: true}
	source := NewLinearWatcherSource(linearService, nopLogger(t))
	starter := &fakeTaskStarter{}
	coordinator := newTestCoordinator(t, &fakeTaskCreator{}, true, starter)
	coordinator.Dispatch(context.Background(), source, sampleLinearEvent())
	if len(linearService.gotAssign) != 1 || starter.called != 0 {
		t.Fatalf("association=%v, starts=%d", linearService.gotAssign, starter.called)
	}
}

type recoveryTaskCreator struct {
	fakeTaskCreator
	found bool
}

func (c *recoveryTaskCreator) FindExistingIssueTask(context.Context, string, string) (bool, error) {
	return c.found, nil
}

func TestLinearRecoveryBypassesCapacityOnlyForExistingTask(t *testing.T) {
	event := sampleLinearEvent()
	event.Issue.RecoveryPending = true
	cap := 1
	event.MaxInflightTasks = &cap
	request, err := (&LinearWatcherSource{}).BuildTaskRequest(event)
	if err != nil {
		t.Fatal(err)
	}
	creator := &recoveryTaskCreator{}
	got, err := watcherRecoveryEvent(context.Background(), creator, event, request)
	if err != nil || got.(*linear.NewLinearIssueEvent).MaxInflightTasks == nil {
		t.Fatalf("missing task must retain capacity limit: %v", err)
	}
	creator.found = true
	got, err = watcherRecoveryEvent(context.Background(), creator, event, request)
	if err != nil || got.(*linear.NewLinearIssueEvent).MaxInflightTasks != nil {
		t.Fatalf("existing task must bypass capacity limit: %v", err)
	}
}
