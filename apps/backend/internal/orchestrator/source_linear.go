package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/kandev/kandev/internal/common/logger"
	"github.com/kandev/kandev/internal/linear"
	"github.com/kandev/kandev/internal/task/models"
)

// linearWatchMetadataKey is the task-metadata key a Linear watcher-created
// task records its originating watch id under. Shared by BuildTaskRequest
// (writer) and WatchMetadataKey (read by the throttle gate's task counter).
const linearWatchMetadataKey = "linear_issue_watch_id"

// LinearWatcherSource adapts the Linear integration onto the WatcherSource
// pipeline. Wraps the existing LinearService (already used by the orchestrator
// for dedup) and the package-local interpolateLinearPrompt so behaviour is
// preserved byte-for-byte versus the old createLinearIssueTask helper.
type LinearWatcherSource struct {
	service LinearService
	logger  *logger.Logger
}

// NewLinearWatcherSource constructs a source bound to the orchestrator's
// Linear service handle. logger may be nil — methods that log will no-op.
func NewLinearWatcherSource(svc LinearService, log *logger.Logger) *LinearWatcherSource {
	return &LinearWatcherSource{service: svc, logger: log}
}

func (s *LinearWatcherSource) Name() string { return "linear" }

func (s *LinearWatcherSource) Reserve(ctx context.Context, evt any) (bool, error) {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil || e.Issue == nil {
		return false, errors.New("linear source: event payload missing or wrong type")
	}
	if s.service == nil {
		return false, errors.New("linear source: service unavailable")
	}
	return s.service.ReserveIssueWatchTask(ctx, e.IssueWatchID, e.Issue)
}

func (s *LinearWatcherSource) Release(_ context.Context, _ any) {
	// The reservation survives uncertain creates and is retried using external_id.
}

func (s *LinearWatcherSource) BuildTaskRequest(evt any) (*IssueTaskRequest, error) {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil || e.Issue == nil {
		return nil, errors.New("linear source: event payload missing or wrong type")
	}
	orgID, err := uuid.Parse(e.Issue.OrganizationID)
	if err != nil {
		return nil, errors.New("linear source: invalid organization ID")
	}
	issueID, err := uuid.Parse(e.Issue.ID)
	if err != nil {
		return nil, errors.New("linear source: invalid issue ID")
	}
	identifier, issueURL := e.Issue.Identifier, e.Issue.URL
	if e.Issue.ReservationIdentifier != "" {
		identifier, issueURL = e.Issue.ReservationIdentifier, e.Issue.ReservationURL
	}
	req := &IssueTaskRequest{
		WorkspaceID:    e.WorkspaceID,
		WorkflowID:     e.WorkflowID,
		WorkflowStepID: e.WorkflowStepID,
		Title:          fmt.Sprintf("[%s] %s", e.Issue.Identifier, e.Issue.Title),
		Description:    interpolateLinearPrompt(e.Prompt, e.Issue),
		ExternalID:     "linear-watch:" + orgID.String() + ":" + issueID.String(),
		Metadata: map[string]interface{}{
			linearWatchMetadataKey:          e.IssueWatchID,
			"linear_issue_identifier":       identifier,
			"linear_issue_url":              issueURL,
			"linear_state":                  e.Issue.StateName,
			"linear_assignee":               e.Issue.AssigneeName,
			models.MetaKeyAgentProfileID:    e.AgentProfileID,
			models.MetaKeyExecutorProfileID: e.ExecutorProfileID,
		},
	}
	// Only a bound watch carries Repositories. An unbound watch leaves the slice
	// nil so the launch path falls to the historical blank-scratch behaviour.
	if e.RepositoryID != "" {
		req.Repositories = []IssueTaskRepository{{
			RepositoryID: e.RepositoryID,
			BaseBranch:   e.BaseBranch,
		}}
	}
	return req, nil
}

func (s *LinearWatcherSource) AttachTaskID(ctx context.Context, evt any, taskID string) error {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil || e.Issue == nil || s.service == nil {
		return nil
	}
	identifier := e.Issue.Identifier
	if e.Issue.ReservationIdentifier != "" {
		identifier = e.Issue.ReservationIdentifier
	}
	return s.service.AssignIssueWatchTaskID(ctx, e.IssueWatchID, identifier, e.Issue.ReservationID, taskID)
}

// A Linear task must not auto-start until Host writeback can resolve its source.
func (s *LinearWatcherSource) IsTerminalAttachError(error) bool { return true }

func (s *LinearWatcherSource) SuppressWatcherAutoStart() bool { return true }

func (s *LinearWatcherSource) WatchID(evt any) string {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil {
		return ""
	}
	return e.IssueWatchID
}

func (s *LinearWatcherSource) MaxInflightTasks(evt any) *int {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil {
		return nil
	}
	return e.MaxInflightTasks
}

// WatchMetadataKey returns the task-metadata key this source writes in
// BuildTaskRequest, used by the throttle gate to count open Linear tasks.
func (s *LinearWatcherSource) WatchMetadataKey() string { return linearWatchMetadataKey }

func (s *LinearWatcherSource) AutoStartParams(evt any) AutoStartParams {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil {
		return AutoStartParams{}
	}
	return AutoStartParams{
		AgentProfileID:    e.AgentProfileID,
		ExecutorProfileID: e.ExecutorProfileID,
		WorkflowStepID:    e.WorkflowStepID,
	}
}

// AgentProfileID returns the watcher's bound profile id, or "" when the
// event payload is malformed. Pre-flight uses "" as the skip-check signal.
func (s *LinearWatcherSource) AgentProfileID(evt any) string {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil {
		return ""
	}
	return e.AgentProfileID
}

// SelfHeal disables the linear_issue_watches row that produced this event.
// Nil-safe: with no LinearService wired the call is silently dropped — same
// pattern as Reserve / Release.
func (s *LinearWatcherSource) SelfHeal(ctx context.Context, evt any, cause string) error {
	e, ok := evt.(*linear.NewLinearIssueEvent)
	if !ok || e == nil || s.service == nil {
		return nil
	}
	return s.service.DisableIssueWatchWithError(ctx, e.IssueWatchID, cause)
}
