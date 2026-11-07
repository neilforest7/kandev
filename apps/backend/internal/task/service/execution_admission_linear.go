package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kandev/kandev/internal/execadmission"
	"github.com/kandev/kandev/internal/task/models"
	"go.uber.org/zap"
)

var (
	errLinearExecutionApprovalRequired = errors.New("linear watch execution requires an approved plan")
	errLinearExecutionApprovalChanged  = errors.New("linear watch execution approval changed")
)

// SetExecutionApproval installs the host side of the Linear execution approval.
// source resolves the host-owned issue association of a watch root, and
// investigation lists the exact profile ids allowed to run the read-only
// investigation phase. Without a bridge every watch execution stays denied.
func (s *Service) SetExecutionApproval(
	client *execadmission.Client,
	investigation []string,
	source func(context.Context, string) (execadmission.NativeSource, error),
) {
	s.executionApproval = client
	s.executionInvestigationProfiles = investigation
	s.executionSource = source
}

// AdmitExecution decides whether one native execution may start, prompt or
// resume. Every identity is host-derived; a caller supplies only the session,
// task and the final resolved profile the runtime will execute.
func (s *Service) AdmitExecution(ctx context.Context, candidate execadmission.Candidate) error {
	root, ancestry, err := s.linearWatchChain(ctx, candidate.TaskID)
	if err != nil {
		return err
	}
	if root == nil {
		return nil
	}
	if err := execadmission.DenyTerminalAncestry(ancestry); err != nil {
		return err
	}
	session, err := s.admissionSession(ctx, candidate)
	if err != nil {
		return err
	}
	pin, pinned, err := linearExecutionPin(session.Metadata)
	if err != nil {
		return err
	}
	intent, err := execadmission.IntentFor(candidate.Profile, s.executionInvestigationProfiles)
	if err != nil {
		return err
	}
	if pinned {
		if err := admitPinnedExecution(pin, intent, candidate.Profile); err != nil {
			return err
		}
	}
	result, err := s.checkLinearExecution(ctx, root, candidate, pin.PlanVersion, intent)
	if err != nil {
		return err
	}
	next := execadmission.Pin{
		ApprovalID:  result.ApprovalID,
		PlanVersion: result.PlanVersion,
		Intent:      intent,
		Profile:     candidate.Profile,
	}
	if pinned {
		if next != pin {
			return errLinearExecutionApprovalChanged
		}
		return nil
	}
	return s.pinLinearExecution(ctx, candidate.SessionID, next)
}

// admissionSession requires the named session to belong to the admitted task.
func (s *Service) admissionSession(ctx context.Context, candidate execadmission.Candidate) (*models.TaskSession, error) {
	if candidate.SessionID == "" || candidate.Profile.ID == "" {
		return nil, errLinearExecutionApprovalRequired
	}
	session, err := s.sessions.GetTaskSession(ctx, candidate.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.TaskID != candidate.TaskID {
		return nil, errors.New("linear watch execution session does not belong to the task")
	}
	return session, nil
}

// admitPinnedExecution keeps a pinned session on its approved intent and exact
// configuration.
func admitPinnedExecution(pin execadmission.Pin, intent string, profile execadmission.Profile) error {
	if err := execadmission.DenyIntentChange(pin.Intent, intent); err != nil {
		return err
	}
	return execadmission.DenyProfileDrift(pin.Profile, profile)
}

// checkLinearExecution asks the bridge about the exact host-derived binding.
func (s *Service) checkLinearExecution(ctx context.Context, root *models.Task, candidate execadmission.Candidate, planVersion, intent string) (execadmission.Result, error) {
	if s.executionApproval == nil || s.executionSource == nil {
		return execadmission.Result{}, errLinearExecutionApprovalRequired
	}
	source, err := s.executionSource(ctx, root.ID)
	if err != nil {
		return execadmission.Result{}, fmt.Errorf("resolve linear watch source: %w", err)
	}
	return s.executionApproval.Check(ctx, execadmission.Request{
		OrganizationID: source.OrganizationID,
		IssueID:        source.IssueID,
		RootTaskID:     root.ID,
		WorkspaceID:    source.WorkspaceID,
		TaskID:         candidate.TaskID,
		SessionID:      candidate.SessionID,
		Intent:         intent,
		PlanVersion:    planVersion,
		Profile:        candidate.Profile,
	})
}

// denyUnauthorizedLinearExecution keeps the executor-ownership gate fail-closed
// for a watch session whose agent already started: the final execution gate
// writes the pin before any spawn, so a running session always has one.
func (s *Service) denyUnauthorizedLinearExecution(ctx context.Context, session *models.TaskSession) error {
	if session.AgentExecutionID == "" {
		return nil
	}
	root, _, err := s.linearWatchChain(ctx, session.TaskID)
	if err != nil {
		return err
	}
	if root == nil {
		return nil
	}
	if _, pinned, err := linearExecutionPin(session.Metadata); err != nil {
		return err
	} else if !pinned {
		return errLinearExecutionApprovalRequired
	}
	return nil
}

// linearWatchChain returns the root-to-task ancestry of a task under a genuine
// watch-created root. It returns a nil root for an ordinary task, and denies a
// watch marker under a root that never received its creation receipt.
func (s *Service) linearWatchChain(ctx context.Context, taskID string) (*models.Task, []execadmission.NativeAncestor, error) {
	if taskID == "" {
		return nil, nil, errors.New("execution admission task is unavailable")
	}
	seen := make(map[string]bool, 4)
	var chain []*models.Task
	for id := taskID; id != ""; {
		if seen[id] {
			return nil, nil, errors.New("task ancestry is cyclic")
		}
		seen[id] = true
		task, err := s.tasks.GetTask(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if task == nil {
			return nil, nil, errors.New("task ancestry is unavailable")
		}
		chain = append(chain, task)
		id = task.ParentID
	}
	root := chain[len(chain)-1]
	marked := linearWatchMarked(root)
	if marked && linearWatchCreationReceipt(root) != root.ExternalID {
		return nil, nil, errLinearExecutionApprovalRequired
	}
	if !marked {
		for _, task := range chain[:len(chain)-1] {
			if linearWatchMarked(task) {
				return nil, nil, errLinearExecutionApprovalRequired
			}
		}
		return nil, nil, nil
	}
	ancestry := make([]execadmission.NativeAncestor, 0, len(chain))
	for index := len(chain) - 1; index >= 0; index-- {
		ancestry = append(ancestry, execadmission.NativeAncestor{
			ID:       chain[index].ID,
			State:    chain[index].State,
			Archived: chain[index].ArchivedAt != nil,
		})
	}
	return root, ancestry, nil
}

func linearWatchMarked(task *models.Task) bool {
	watchID, _ := task.Metadata[models.MetaKeyLinearIssueWatchID].(string)
	return watchID != "" || strings.HasPrefix(task.ExternalID, ReservedExternalIDPrefixLinearWatch)
}

// pinLinearExecution stores the admission write-once. A concurrent first
// admission wins only when it stored the identical pin.
func (s *Service) pinLinearExecution(ctx context.Context, sessionID string, pin execadmission.Pin) error {
	writer, ok := s.sessions.(interface {
		SetSessionMetadataKeyIfAbsent(context.Context, string, string, interface{}) (bool, error)
	})
	if !ok {
		return errors.New("linear watch execution pin storage is unavailable")
	}
	if _, err := writer.SetSessionMetadataKeyIfAbsent(ctx, sessionID, models.SessionMetaKeyLinearExecutionPin, pin); err != nil {
		return err
	}
	session, err := s.sessions.GetTaskSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session == nil {
		return errors.New("linear watch execution pin session is unavailable")
	}
	stored, pinned, err := linearExecutionPin(session.Metadata)
	if err != nil {
		return err
	}
	if !pinned || stored != pin {
		return errLinearExecutionApprovalChanged
	}
	return nil
}

// linearExecutionPin decodes the protected pin. A stored value that does not
// decode denies rather than admitting without proof.
func linearExecutionPin(metadata map[string]interface{}) (execadmission.Pin, bool, error) {
	value, ok := metadata[models.SessionMetaKeyLinearExecutionPin]
	if !ok || value == nil {
		return execadmission.Pin{}, false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return execadmission.Pin{}, false, fmt.Errorf("read linear execution pin: %w", err)
	}
	var pin execadmission.Pin
	if err := json.Unmarshal(encoded, &pin); err != nil {
		return execadmission.Pin{}, false, fmt.Errorf("decode linear execution pin: %w", err)
	}
	if pin.Intent == "" || pin.Profile.ID == "" || pin.Profile.PermissionMode == "" || pin.Profile.ConfigDigest == "" {
		return execadmission.Pin{}, false, errors.New("linear execution pin is incomplete")
	}
	return pin, true, nil
}

// runLinearExecutionWatchdog stops running watch executions whose approval no
// longer holds: a paused, revoked, superseded, stale or unreachable bridge
// decision. It runs on the session reconciliation tick, after the sweep fetched
// nothing it can reuse.
//
// shortcut: the candidate list is re-read here instead of threaded out of the
// active-session sweep; merge the two passes if this scan ever gets expensive.
func (s *Service) runLinearExecutionWatchdog(ctx context.Context) {
	if s.executionApproval == nil || s.executionSource == nil || s.executionStopper == nil {
		return
	}
	sessions, err := s.linearExecutionCandidateSessions(ctx)
	if err != nil {
		s.logger.Error("linear execution watchdog: failed to read candidates", zap.Error(err))
		return
	}
	for _, session := range sessions {
		s.enforceLinearExecutionPin(ctx, session)
	}
}

// linearExecutionCandidateSessions lists active sessions of unarchived tasks.
func (s *Service) linearExecutionCandidateSessions(ctx context.Context) ([]*models.TaskSession, error) {
	tasks, err := s.tasks.ListUnarchivedTasksWithActiveSessions(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task != nil && task.ID != "" {
			ids = append(ids, task.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	sessionsByTask, err := s.sessions.BatchGetSessionsByTaskIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	var candidates []*models.TaskSession
	for _, sessions := range sessionsByTask {
		for _, session := range sessions {
			if session != nil && linearExecutionSessionActive(session.State) {
				candidates = append(candidates, session)
			}
		}
	}
	return candidates, nil
}

// enforceLinearExecutionPin re-checks one session and stops its execution when
// the approval no longer holds.
func (s *Service) enforceLinearExecutionPin(ctx context.Context, session *models.TaskSession) {
	// Background checks run as the session's durable actor, never as an
	// anonymous caller and never as whoever last touched the request.
	ctx = models.WithExecutorActor(ctx, session.ExecutorActorUserID)
	pin, pinned, err := linearExecutionPin(session.Metadata)
	if err != nil {
		s.logger.Error("linear execution watchdog: unreadable pin", zap.String("session_id", session.ID), zap.Error(err))
	}
	if !pinned {
		return
	}
	if s.executionConfigValidator != nil {
		if err := s.executionConfigValidator(ctx, session.ID); err != nil {
			s.stopRevokedLinearExecution(ctx, session, err)
			return
		}
	}
	if err := s.AdmitExecution(ctx, execadmission.Candidate{SessionID: session.ID, TaskID: session.TaskID, Profile: pin.Profile}); err != nil {
		s.stopRevokedLinearExecution(ctx, session, err)
	}
}

func linearExecutionSessionActive(state models.TaskSessionState) bool {
	switch state {
	case models.TaskSessionStateCreated, models.TaskSessionStateStarting, models.TaskSessionStateRunning, models.TaskSessionStateWaitingForInput:
		return true
	default:
		return false
	}
}

// stopRevokedLinearExecution issues an exact stop for the observed execution
// only. A session without an execution id stays blocked: a session-scoped stop
// could target a replacement that the refusal never covered.
func (s *Service) stopRevokedLinearExecution(ctx context.Context, session *models.TaskSession, admissionErr error) {
	executionID := session.AgentExecutionID
	if executionID == "" {
		s.logger.Warn("linear execution watchdog: blocked execution id is unknown",
			zap.String("session_id", session.ID),
			zap.String("admission_error", admissionErr.Error()))
		return
	}
	current, err := s.sessions.GetTaskSession(ctx, session.ID)
	if err != nil || current == nil {
		s.logger.Warn("linear execution watchdog: slot identity is unreadable",
			zap.String("session_id", session.ID),
			zap.String("execution_id", executionID),
			zap.Error(err))
		return
	}
	if current.AgentExecutionID != executionID {
		s.logger.Warn("linear execution watchdog: slot was replaced before the stop",
			zap.String("session_id", session.ID),
			zap.String("observed_execution_id", executionID),
			zap.String("current_execution_id", current.AgentExecutionID))
		return
	}
	reason := "linear approval is no longer valid"
	if err := s.executionStopper.StopExecution(ctx, executionID, reason, true); err != nil {
		s.logger.Warn("linear execution watchdog: stop was not confirmed",
			zap.String("session_id", session.ID),
			zap.String("execution_id", executionID),
			zap.String("admission_error", admissionErr.Error()),
			zap.Error(err))
		return
	}
	s.logger.Info("linear execution watchdog stopped an unapproved execution",
		zap.String("session_id", session.ID),
		zap.String("execution_id", executionID),
		zap.String("admission_error", admissionErr.Error()))
}

// linearExecutionWatchdogInterval keeps the admission freshness window short
// without tying it to the heavy one-minute reconciliation pass.
const linearExecutionWatchdogInterval = 10 * time.Second

// StartLinearExecutionWatchdog runs the admission watchdog on its own fixed
// tick for the lifetime of the manager context.
func (s *Service) StartLinearExecutionWatchdog(ctx context.Context) {
	if s.executionApproval == nil || s.executionSource == nil || s.executionStopper == nil {
		return
	}
	ticker := time.NewTicker(linearExecutionWatchdogInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runLinearExecutionWatchdog(ctx)
			}
		}
	}()
	s.logger.Info("linear execution watchdog started (every 10 seconds)")
}

// SetExecutionConfigValidator installs the shared runtime validator the watchdog
// uses to re-check live configuration against the recorded admission.
func (s *Service) SetExecutionConfigValidator(validate func(context.Context, string) error) {
	s.executionConfigValidator = validate
}

// LinearExecutionScope answers the runtime's scope question before it resolves
// any profile: only work under a genuine watch root is subject to the trusted
// gate, so ordinary launches keep working with base-model profiles.
//
// The native session is resolved first: a session that belongs to a watch task
// is in scope even when the caller omitted the task, and a supplied task that
// disagrees with the session binding is refused. Only when both identities are
// absent does a legacy taskless launch stay out of scope.
func (s *Service) LinearExecutionScope(ctx context.Context, taskID, sessionID string) (bool, error) {
	effectiveTaskID := taskID
	if sessionID != "" {
		session, err := s.sessions.GetTaskSession(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if session != nil {
			if taskID != "" && session.TaskID != taskID {
				return false, errors.New("execution admission session does not belong to the task")
			}
			effectiveTaskID = session.TaskID
		}
	}
	if effectiveTaskID == "" {
		return false, nil
	}
	root, _, err := s.linearWatchChain(ctx, effectiveTaskID)
	if err != nil {
		return false, err
	}
	return root != nil, nil
}
