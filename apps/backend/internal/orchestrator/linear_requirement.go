package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kandev/kandev/internal/linear"
	"github.com/kandev/kandev/internal/task/models"
	taskservice "github.com/kandev/kandev/internal/task/service"
)

// metaKeyLinearRequirementDigest records the digest of the Linear title and
// description the task description was last rendered from. The approval bridge
// compares it with the live issue before it publishes a plan question.
const metaKeyLinearRequirementDigest = "linear_requirement_digest"

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

// linearRequirementDigest is the sha256 of the canonical JSON {title, description};
// it must stay identical to requirementDigest in the bridge (src/projection.ts).
func linearRequirementDigest(issue *linear.LinearIssue) string {
	canonical, _ := json.Marshal(struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}{issue.Title, issue.Description})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// refreshLinearRequirement re-renders a Linear watch root's description from the
// live issue with the watch's own template, so a workflow entry never prompts the
// agent with a requirement that changed after the task was created. Tasks without
// a Linear watch link are returned untouched. A failed source read is an error:
// the caller must not launch on a requirement it could not confirm.
func (s *Service) refreshLinearRequirement(ctx context.Context, task *models.Task) (*models.Task, error) {
	if task == nil || task.ParentID != "" || s.linearService == nil {
		return task, nil
	}
	if _, watched := task.Metadata[linearWatchMetadataKey]; !watched {
		return task, nil
	}
	snapshot, err := s.linearService.GetTaskIssueSnapshot(ctx, task.ID)
	if errors.Is(err, linear.ErrSourceLinkNotFound) {
		return task, nil
	}
	if err != nil {
		return nil, fmt.Errorf("refresh linear requirement: %w", err)
	}
	description := interpolateLinearPrompt(snapshot.Prompt, snapshot.Issue)
	digest := linearRequirementDigest(snapshot.Issue)
	current, _ := task.Metadata[metaKeyLinearRequirementDigest].(string)
	if task.Description == description && current == digest {
		return task, nil
	}
	if s.linearRequirementTasks == nil {
		return nil, errors.New("refresh linear requirement: task service unavailable")
	}
	if task.Description != description {
		if _, err := s.linearRequirementTasks.UpdateTask(ctx, task.ID, &taskservice.UpdateTaskRequest{Description: &description}); err != nil {
			return nil, fmt.Errorf("refresh linear requirement: %w", err)
		}
	}
	if err := s.repo.SetTaskMetadataKey(ctx, task.ID, metaKeyLinearRequirementDigest, digest); err != nil {
		return nil, fmt.Errorf("refresh linear requirement: %w", err)
	}
	reloaded, err := s.repo.GetTask(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("refresh linear requirement: %w", err)
	}
	return reloaded, nil
}

// refreshLinearRequirementForStart refreshes the task behind a launch and swaps
// in the new description when the caller's prompt was taken from the old one.
func (s *Service) refreshLinearRequirementForStart(ctx context.Context, taskID, prompt string) (string, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return prompt, nil // the launch path reports a missing task itself
	}
	refreshed, err := s.refreshLinearRequirement(ctx, task)
	if err != nil {
		return "", err
	}
	if prompt == task.Description {
		return refreshed.Description, nil
	}
	return prompt, nil
}
