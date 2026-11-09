package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kandev/kandev/internal/execadmission"
	"github.com/kandev/kandev/internal/task/models"
	sqliterepo "github.com/kandev/kandev/internal/task/repository/sqlite"
	v1 "github.com/kandev/kandev/pkg/api/v1"
	"github.com/stretchr/testify/require"
)

const gateWorkspace = "linear-gate-ws"

type linearGateFixture struct {
	svc       *Service
	repo      *sqliterepo.Repository
	rootID    string
	childID   string
	sessionID string // session of the child task

	mu       sync.Mutex
	requests []gateWireRequest
	reply    string
	status   int
}

// gateWireRequest mirrors the bridge's check payload, where the profile travels
// as effectiveProfile.
type gateWireRequest struct {
	OrganizationID   string                `json:"organizationId"`
	IssueID          string                `json:"issueId"`
	RootTaskID       string                `json:"rootTaskId"`
	WorkspaceID      string                `json:"workspaceId"`
	TaskID           string                `json:"taskId"`
	SessionID        string                `json:"sessionId"`
	Intent           string                `json:"intent"`
	PlanVersion      string                `json:"planVersion"`
	EffectiveProfile execadmission.Profile `json:"effectiveProfile"`
}

func newLinearGateFixture(t *testing.T, investigationProfiles ...string) *linearGateFixture {
	t.Helper()
	ctx := context.Background()
	svc, _, repo := createTestService(t)
	workflowID := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, gateWorkspace)
	externalID := ReservedExternalIDPrefixLinearWatch + "org:issue"
	root, err := svc.CreateTask(ctx, &CreateTaskRequest{
		WorkspaceID: gateWorkspace, WorkflowID: workflowID, Title: "Issue",
		ExternalID: externalID, AllowReservedExternalID: true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.MarkLinearWatchCreationComplete(ctx, root.Task.ID, externalID))
	child, err := svc.CreateTask(ctx, &CreateTaskRequest{
		WorkspaceID: gateWorkspace, WorkflowID: workflowID, Title: "Child", ParentID: root.Task.ID,
	})
	require.NoError(t, err)
	sessionID := "gate-session"
	require.NoError(t, repo.CreateTaskSession(ctx, &models.TaskSession{ID: sessionID, TaskID: child.Task.ID}))

	fixture := &linearGateFixture{
		svc: svc, repo: repo, rootID: root.Task.ID, childID: child.Task.ID, sessionID: sessionID,
		reply: `{"approved":true,"reason":"ok","approvalId":"approval-1","planVersion":"plan-7"}`,
	}
	bridge := httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(bridge.Close)
	client, err := execadmission.NewClient(execadmission.Config{BaseURL: bridge.URL, Token: "token"})
	require.NoError(t, err)
	svc.SetExecutionApproval(client, investigationProfiles, func(context.Context, string) (execadmission.NativeSource, error) {
		return execadmission.NativeSource{OrganizationID: "org-1", IssueID: "issue-1", WorkspaceID: gateWorkspace}, nil
	})
	return fixture
}

func (f *linearGateFixture) handle(w http.ResponseWriter, r *http.Request) {
	var request gateWireRequest
	_ = json.NewDecoder(r.Body).Decode(&request)
	f.mu.Lock()
	f.requests = append(f.requests, request)
	reply, status := f.reply, f.status
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(reply))
}

func (f *linearGateFixture) setReply(reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply = reply
}

func (f *linearGateFixture) recorded() []gateWireRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gateWireRequest(nil), f.requests...)
}

func (f *linearGateFixture) pin(t *testing.T) (execadmission.Pin, bool) {
	t.Helper()
	session, err := f.repo.GetTaskSession(context.Background(), f.sessionID)
	require.NoError(t, err)
	pin, pinned, err := linearExecutionPin(session.Metadata)
	require.NoError(t, err)
	return pin, pinned
}

func gateProfile(t *testing.T, model, mode string) execadmission.Profile {
	t.Helper()
	profile, err := (execadmission.Resolved{
		ProfileID: "profile-1", Agent: "claude", Model: model, Mode: mode, Runtime: "local",
		ConfigOptions: map[string]string{"reasoning_effort": "high"},
	}).EffectiveProfile()
	require.NoError(t, err)
	return profile
}

func (f *linearGateFixture) admit(profile execadmission.Profile) error {
	return f.svc.AdmitExecution(context.Background(), execadmission.Candidate{
		SessionID: f.sessionID, TaskID: f.childID, Profile: profile,
	})
}

func TestLinearExecutionFirstAdmissionPinsPlanVersion(t *testing.T) {
	fixture := newLinearGateFixture(t)
	profile := gateProfile(t, "claude-sonnet-4-6", "agent")

	require.NoError(t, fixture.admit(profile))
	pin, pinned := fixture.pin(t)
	require.True(t, pinned)
	require.Equal(t, "plan-7", pin.PlanVersion)
	require.Equal(t, "approval-1", pin.ApprovalID)
	require.Equal(t, execadmission.IntentImplementation, pin.Intent)
	require.Equal(t, profile, pin.Profile)

	requests := fixture.recorded()
	require.Len(t, requests, 1)
	require.Equal(t, "", requests[0].PlanVersion, "the first admission may omit the version")
	require.Equal(t, fixture.rootID, requests[0].RootTaskID)
	require.Equal(t, fixture.childID, requests[0].TaskID)
	require.Equal(t, "org-1", requests[0].OrganizationID)
	require.Equal(t, "issue-1", requests[0].IssueID)
	require.Equal(t, gateWorkspace, requests[0].WorkspaceID)
	require.Equal(t, profile, requests[0].EffectiveProfile)

	require.NoError(t, fixture.admit(profile))
	requests = fixture.recorded()
	require.Len(t, requests, 2)
	require.Equal(t, "plan-7", requests[1].PlanVersion, "later admissions pin the version")
}

func TestLinearExecutionOrdinaryTaskSkipsTheGate(t *testing.T) {
	ctx := context.Background()
	svc, _, repo := createTestService(t)
	workflowID := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, "ordinary-ws")
	task, err := svc.CreateTask(ctx, &CreateTaskRequest{WorkspaceID: "ordinary-ws", WorkflowID: workflowID, Title: "Plain"})
	require.NoError(t, err)
	require.NoError(t, repo.CreateTaskSession(ctx, &models.TaskSession{ID: "ordinary-session", TaskID: task.Task.ID}))
	require.NoError(t, svc.AdmitExecution(ctx, execadmission.Candidate{
		SessionID: "ordinary-session", TaskID: task.Task.ID, Profile: gateProfile(t, "model", "agent"),
	}))
}

func TestLinearExecutionTerminalAncestryDenies(t *testing.T) {
	fixture := newLinearGateFixture(t)
	require.NoError(t, fixture.repo.UpdateTaskState(context.Background(), fixture.rootID, v1.TaskStateCompleted))
	err := fixture.admit(gateProfile(t, "model", "agent"))
	require.Error(t, err)
	require.Empty(t, fixture.recorded(), "a terminal source never reaches the bridge")
}

func TestLinearExecutionRequiresGenuineCreationReceipt(t *testing.T) {
	ctx := context.Background()
	svc, _, repo := createTestService(t)
	workflowID := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, "no-receipt-ws")
	externalID := ReservedExternalIDPrefixLinearWatch + "org:issue"
	root, err := svc.CreateTask(ctx, &CreateTaskRequest{
		WorkspaceID: "no-receipt-ws", WorkflowID: workflowID, Title: "Issue",
		ExternalID: externalID, AllowReservedExternalID: true,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateTaskSession(ctx, &models.TaskSession{ID: "no-receipt-session", TaskID: root.Task.ID}))
	err = svc.AdmitExecution(ctx, execadmission.Candidate{
		SessionID: "no-receipt-session", TaskID: root.Task.ID, Profile: gateProfile(t, "model", "agent"),
	})
	require.ErrorIs(t, err, errLinearExecutionApprovalRequired)
}

func TestLinearExecutionInvestigationProofAndEscalation(t *testing.T) {
	investigation := gateProfile(t, "model", "read-only")
	fixture := newLinearGateFixture(t)
	// Re-install with the exact profile id allowlisted as the private proof.
	fixture.svc.SetExecutionApproval(fixture.svc.executionApproval, []string{investigation.ID}, fixture.svc.executionSource)
	fixture.setReply(`{"approved":true,"reason":"ok","approvalId":null,"planVersion":"","basis":"investigation-proof"}`)

	require.NoError(t, fixture.admit(investigation))
	pin, pinned := fixture.pin(t)
	require.True(t, pinned)
	require.Equal(t, execadmission.IntentInvestigation, pin.Intent)
	require.Empty(t, pin.PlanVersion)
	require.Empty(t, pin.ApprovalID)

	err := fixture.admit(gateProfile(t, "model", "agent"))
	require.Error(t, err, "a read-only session cannot escalate to implementation")
	require.Len(t, fixture.recorded(), 1, "escalation is refused before the bridge")
}

func TestLinearExecutionBridgeRefusalKeepsPin(t *testing.T) {
	fixture := newLinearGateFixture(t)
	profile := gateProfile(t, "model", "agent")
	require.NoError(t, fixture.admit(profile))

	fixture.setReply(`{"approved":false,"reason":"revoked","approvalId":"approval-1","planVersion":"plan-7"}`)
	require.Error(t, fixture.admit(profile))
	pin, pinned := fixture.pin(t)
	require.True(t, pinned)
	require.Equal(t, "plan-7", pin.PlanVersion)
	require.Equal(t, "approval-1", pin.ApprovalID)
	recorded := fixture.recorded()
	require.Len(t, recorded, 2)
	require.Equal(t, "plan-7", recorded[1].PlanVersion)
}

func TestLinearExecutionProfileDriftDenies(t *testing.T) {
	fixture := newLinearGateFixture(t)
	require.NoError(t, fixture.admit(gateProfile(t, "model-a", "agent")))
	require.Error(t, fixture.admit(gateProfile(t, "model-b", "agent")))
	require.Len(t, fixture.recorded(), 1, "a drifted profile is refused before the bridge")
}

func TestLinearExecutionPinSurvivesGenericMetadataWrites(t *testing.T) {
	fixture := newLinearGateFixture(t)
	require.NoError(t, fixture.admit(gateProfile(t, "model", "agent")))
	ctx := context.Background()

	// A forged pin from a generic replace is stripped; only the trusted writer
	// may create one.
	require.NoError(t, fixture.repo.UpdateSessionMetadata(ctx, fixture.sessionID, map[string]interface{}{
		"plan_mode":                             true,
		models.SessionMetaKeyLinearExecutionPin: map[string]interface{}{"planVersion": "forged"},
	}))
	session, err := fixture.repo.GetTaskSession(ctx, fixture.sessionID)
	require.NoError(t, err)
	pin, pinned, err := linearExecutionPin(session.Metadata)
	require.NoError(t, err)
	require.True(t, pinned)
	require.Equal(t, "plan-7", pin.PlanVersion, "a generic replace cannot forge or change the pin")
	require.NotNil(t, session.Metadata["plan_mode"], "unrelated caller metadata is kept")
	pin, pinned = fixture.pin(t)
	require.True(t, pinned)
	require.Equal(t, "plan-7", pin.PlanVersion)

	session.UpdatedAt = time.Now().UTC().Add(-time.Minute)
	changed, err := fixture.repo.UpdateTaskSessionIfCurrentStateRemovingMetadataKeys(
		ctx, session, session.State, []string{models.SessionMetaKeyLinearExecutionPin},
	)
	require.NoError(t, err)
	require.True(t, changed)
	pin, pinned = fixture.pin(t)
	require.True(t, pinned, "a metadata deletion cannot drop the pin")
	require.Equal(t, "plan-7", pin.PlanVersion)
}

type recordingExecutionStopper struct {
	executions []string
	sessions   []string
	fail       error
}

func (r *recordingExecutionStopper) StopTask(context.Context, string, string, bool) error { return nil }
func (r *recordingExecutionStopper) StopSession(_ context.Context, sessionID, _ string, _ bool) error {
	r.sessions = append(r.sessions, sessionID)
	return r.fail
}
func (r *recordingExecutionStopper) StopExecution(_ context.Context, executionID, _ string, _ bool) error {
	r.executions = append(r.executions, executionID)
	return r.fail
}
func (r *recordingExecutionStopper) RegisterExecutionStopOwner(string, string, bool) {}

func TestLinearExecutionWatchdogStopsOnlyTheObservedExecution(t *testing.T) {
	fixture := newLinearGateFixture(t)
	profile := gateProfile(t, "model", "agent")
	require.NoError(t, fixture.admit(profile))
	require.NoError(t, fixture.repo.UpsertExecutorRunning(context.Background(), &models.ExecutorRunning{
		TaskID: fixture.childID, SessionID: fixture.sessionID, AgentExecutionID: "execution-observed", Status: models.ExecutorRunningStatusRunning,
	}))
	fixture.setReply(`{"approved":false,"reason":"revoked","approvalId":"approval-1","planVersion":"plan-7"}`)
	stopper := &recordingExecutionStopper{}
	fixture.svc.executionStopper = stopper

	fixture.svc.runLinearExecutionWatchdog(context.Background())
	require.Equal(t, []string{"execution-observed"}, stopper.executions)
	require.Empty(t, stopper.sessions, "a refused approval never stops by session")

	// An unconfirmed stop stays blocked and is retried on the next tick.
	stopper.fail = errors.New("stop not confirmed")
	fixture.svc.runLinearExecutionWatchdog(context.Background())
	require.Equal(t, []string{"execution-observed", "execution-observed"}, stopper.executions)
	require.Empty(t, stopper.sessions)
}

func TestLinearExecutionWatchdogNeverStopsBySessionWhenExecutionIDIsUnknown(t *testing.T) {
	fixture := newLinearGateFixture(t)
	profile := gateProfile(t, "model", "agent")
	require.NoError(t, fixture.admit(profile))
	fixture.setReply(`{"approved":false,"reason":"paused","approvalId":"approval-1","planVersion":"plan-7"}`)
	stopper := &recordingExecutionStopper{}
	fixture.svc.executionStopper = stopper

	fixture.svc.runLinearExecutionWatchdog(context.Background())
	require.Empty(t, stopper.executions)
	require.Empty(t, stopper.sessions, "an unknown execution id must not fall back to a session stop")
}

func TestLinearExecutionWatchdogUsesTheSharedConfigValidator(t *testing.T) {
	fixture := newLinearGateFixture(t)
	profile := gateProfile(t, "model", "agent")
	require.NoError(t, fixture.admit(profile))
	require.NoError(t, fixture.repo.UpsertExecutorRunning(context.Background(), &models.ExecutorRunning{
		TaskID: fixture.childID, SessionID: fixture.sessionID, AgentExecutionID: "execution-observed", Status: models.ExecutorRunningStatusRunning,
	}))
	stopper := &recordingExecutionStopper{}
	fixture.svc.executionStopper = stopper
	validated := 0
	fixture.svc.SetExecutionConfigValidator(func(_ context.Context, sessionID string) error {
		validated++
		require.Equal(t, fixture.sessionID, sessionID)
		return errors.New("execution profile changed since it was admitted")
	})

	fixture.svc.runLinearExecutionWatchdog(context.Background())
	require.Equal(t, 1, validated)
	require.Equal(t, []string{"execution-observed"}, stopper.executions)
	require.Empty(t, stopper.sessions)
}

func TestLinearExecutionScopeUsesTheNativeSessionFirst(t *testing.T) {
	fixture := newLinearGateFixture(t)
	ctx := context.Background()

	// A supplied session naming a watch task is in scope even without a task id.
	inScope, err := fixture.svc.LinearExecutionScope(ctx, "", fixture.sessionID)
	require.NoError(t, err)
	require.True(t, inScope, "a watch session cannot escape by omitting the task")

	// A supplied task that disagrees with the session binding is refused.
	_, err = fixture.svc.LinearExecutionScope(ctx, "another-task", fixture.sessionID)
	require.Error(t, err)

	// An ordinary session remains out of scope.
	ordinarySvc, _, ordinaryRepo := createTestService(t)
	workflowID := seedWorkspaceAndWorkflowForCreate(t, ctx, ordinaryRepo, "scope-ordinary-ws")
	task, err := ordinarySvc.CreateTask(ctx, &CreateTaskRequest{WorkspaceID: "scope-ordinary-ws", WorkflowID: workflowID, Title: "Plain"})
	require.NoError(t, err)
	require.NoError(t, ordinaryRepo.CreateTaskSession(ctx, &models.TaskSession{ID: "scope-ordinary-session", TaskID: task.Task.ID}))
	inScope, err = ordinarySvc.LinearExecutionScope(ctx, "", "scope-ordinary-session")
	require.NoError(t, err)
	require.False(t, inScope)

	// Both identities empty stays out of scope (legacy taskless launch).
	inScope, err = ordinarySvc.LinearExecutionScope(ctx, "", "")
	require.NoError(t, err)
	require.False(t, inScope)
}

func TestLinearExecutionPinCannotBeInsertedByGenericReplace(t *testing.T) {
	ctx := context.Background()
	svc, _, repo := createTestService(t)
	workflowID := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, "pin-insert-ws")
	externalID := ReservedExternalIDPrefixLinearWatch + "org:issue"
	root, err := svc.CreateTask(ctx, &CreateTaskRequest{
		WorkspaceID: "pin-insert-ws", WorkflowID: workflowID, Title: "Issue",
		ExternalID: externalID, AllowReservedExternalID: true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.MarkLinearWatchCreationComplete(ctx, root.Task.ID, externalID))
	require.NoError(t, repo.CreateTaskSession(ctx, &models.TaskSession{ID: "pin-insert-session", TaskID: root.Task.ID}))

	// No pin yet: a generic replacement must not create one.
	require.NoError(t, repo.UpdateSessionMetadata(ctx, "pin-insert-session", map[string]interface{}{
		models.SessionMetaKeyLinearExecutionPin: map[string]interface{}{"planVersion": "forged"},
	}))
	session, err := repo.GetTaskSession(ctx, "pin-insert-session")
	require.NoError(t, err)
	_, pinned, err := linearExecutionPin(session.Metadata)
	require.NoError(t, err)
	require.False(t, pinned, "a generic replace cannot insert a protected pin")

	// The trusted admission still creates it afterwards.
	fixture := &linearGateFixture{svc: svc, repo: repo, rootID: root.Task.ID, childID: root.Task.ID, sessionID: "pin-insert-session",
		reply: `{"approved":true,"reason":"ok","approvalId":"approval-1","planVersion":"plan-7"}`}
	bridge := httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(bridge.Close)
	client, err := execadmission.NewClient(execadmission.Config{BaseURL: bridge.URL, Token: "token"})
	require.NoError(t, err)
	svc.SetExecutionApproval(client, nil, func(context.Context, string) (execadmission.NativeSource, error) {
		return execadmission.NativeSource{OrganizationID: "org-1", IssueID: "issue-1", WorkspaceID: "pin-insert-ws"}, nil
	})
	require.NoError(t, fixture.admit(gateProfile(t, "model", "agent")))
	pin, pinned, err := linearExecutionPin(mustSessionMetadata(t, repo, "pin-insert-session"))
	require.NoError(t, err)
	require.True(t, pinned)
	require.Equal(t, "plan-7", pin.PlanVersion)
}

func mustSessionMetadata(t *testing.T, repo *sqliterepo.Repository, sessionID string) map[string]interface{} {
	t.Helper()
	session, err := repo.GetTaskSession(context.Background(), sessionID)
	require.NoError(t, err)
	return session.Metadata
}
