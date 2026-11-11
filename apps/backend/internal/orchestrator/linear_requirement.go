package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/kandev/kandev/internal/linear"
	"github.com/kandev/kandev/internal/task/models"
	taskservice "github.com/kandev/kandev/internal/task/service"
)

// linearRequirementTaskService persists a refreshed description through the
// task service so listeners see the same task update as any other edit.
type linearRequirementTaskService interface {
	UpdateTask(ctx context.Context, id string, req *taskservice.UpdateTaskRequest) (*models.Task, error)
}

// SetLinearRequirementTaskService wires the task service used by
// refreshLinearRequirement.
func (s *Service) SetLinearRequirementTaskService(svc linearRequirementTaskService) {
	s.linearRequirementTasks = svc
}

// sessionMetaKeyLinearRequirementDigest records, on the session that was sent a
// Linear root's description, the digest of the requirement it was rendered from.
// The approval bridge compares this with the live issue before it publishes the
// plan that session asks about. A session that later receives a different
// requirement is marked conflicting and can no longer publish a plan.
const (
	sessionMetaKeyLinearRequirementDigest = "linear_requirement_digest"
	linearRequirementConflict             = "conflict"
)

// linearRequirement is one rendered snapshot of a Linear issue: the exact
// description text and the digest of the title and description it came from.
// They travel together into prompt composition and the session stamp; a stored
// description is never re-read and paired with a digest from another read.
type linearRequirement struct {
	Description string
	Digest      string
}

type linearRequirementCtxKey struct{}

func withLinearRequirement(ctx context.Context, req *linearRequirement) context.Context {
	if req == nil {
		return ctx
	}
	return context.WithValue(ctx, linearRequirementCtxKey{}, req)
}

func linearRequirementFromContext(ctx context.Context) *linearRequirement {
	req, _ := ctx.Value(linearRequirementCtxKey{}).(*linearRequirement)
	return req
}

func requirementDigestOf(req *linearRequirement) string {
	if req == nil {
		return ""
	}
	return req.Digest
}

func linearRequirementDigestFromContext(ctx context.Context) string {
	if req := linearRequirementFromContext(ctx); req != nil {
		return req.Digest
	}
	return ""
}

// launchDescription is the description a launch falls back to: the rendered
// snapshot when this launch refreshed one, else the stored text.
func launchDescription(ctx context.Context, stored string) string {
	if req := linearRequirementFromContext(ctx); req != nil {
		return req.Description
	}
	return stored
}

// linearRequirementDigest is sha256 over the UTF-8 title, a 0x00 byte and the
// UTF-8 description. The bridge (src/projection.ts) computes the same bytes; the
// shared vectors in both test suites pin the encoding.
func linearRequirementDigest(issue *linear.LinearIssue) string {
	sum := sha256.New()
	sum.Write([]byte(issue.Title))
	sum.Write([]byte{0})
	sum.Write([]byte(issue.Description))
	return hex.EncodeToString(sum.Sum(nil))
}

// refreshLinearRequirement re-renders a Linear watch root's description from the
// live issue with the watch's own template, so a workflow entry never prompts the
// agent with a requirement that changed after the task was created. It returns a
// copy of the task carrying the rendered text, and that snapshot (nil for tasks
// without a Linear watch link, which are left untouched). A failed source read is
// an error: the caller must not launch on a requirement it could not confirm.
func (s *Service) refreshLinearRequirement(ctx context.Context, task *models.Task) (*models.Task, *linearRequirement, error) {
	if task == nil || task.ParentID != "" || s.linearService == nil {
		return task, nil, nil
	}
	if _, watched := task.Metadata[linearWatchMetadataKey]; !watched {
		return task, nil, nil
	}
	snapshot, err := s.linearService.GetTaskIssueSnapshot(ctx, task.ID)
	if errors.Is(err, linear.ErrSourceLinkNotFound) {
		return task, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("refresh linear requirement: %w", err)
	}
	req := &linearRequirement{Description: interpolateLinearPrompt(snapshot.Prompt, snapshot.Issue), Digest: linearRequirementDigest(snapshot.Issue)}
	if task.Description != req.Description {
		if s.linearRequirementTasks == nil {
			return nil, nil, errors.New("refresh linear requirement: task service unavailable")
		}
		if _, err := s.linearRequirementTasks.UpdateTask(ctx, task.ID, &taskservice.UpdateTaskRequest{Description: &req.Description}); err != nil {
			return nil, nil, fmt.Errorf("refresh linear requirement: %w", err)
		}
	}
	rendered := *task
	rendered.Description = req.Description
	return &rendered, req, nil
}

// refreshLinearRequirementForStart refreshes the task behind a launch, swaps in
// the rendered description when the caller's prompt was taken from the old one,
// and carries the snapshot in the context so the launch composes from it and
// stamps its session after admission.
func (s *Service) refreshLinearRequirementForStart(ctx context.Context, taskID, prompt string) (context.Context, string, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return ctx, prompt, nil // the launch path reports a missing task itself
	}
	_, req, err := s.refreshLinearRequirement(ctx, task)
	if err != nil {
		return ctx, "", err
	}
	if req != nil && prompt == task.Description {
		prompt = req.Description
	}
	return withLinearRequirement(ctx, req), prompt, nil
}

// stampLinearRequirement records the dispatched requirement on its session. The
// first digest sticks; a different later one marks the session conflicting, so a
// pending question can never be attributed to a requirement it did not see alone.
func (s *Service) stampLinearRequirement(ctx context.Context, sessionID, digest string) error {
	if digest == "" {
		return nil
	}
	session, err := s.repo.GetTaskSession(ctx, sessionID)
	if err != nil || session == nil {
		return fmt.Errorf("stamp linear requirement: load session %s: %w", sessionID, err)
	}
	existing, _ := session.Metadata[sessionMetaKeyLinearRequirementDigest].(string)
	if existing == digest || existing == linearRequirementConflict {
		return nil
	}
	if existing != "" {
		digest = linearRequirementConflict
	}
	if err := s.repo.SetSessionMetadataKey(ctx, sessionID, sessionMetaKeyLinearRequirementDigest, digest); err != nil {
		return fmt.Errorf("stamp linear requirement: %w", err)
	}
	return nil
}
