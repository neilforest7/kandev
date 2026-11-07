package service

import (
	"context"

	"github.com/kandev/kandev/internal/task/models"
	"github.com/kandev/kandev/internal/task/repository/hierarchy"
)

var errLinearWatchApprovalRequired = hierarchy.ErrLinearWatchApprovalRequired

// denyUnapprovedLinearWatch also covers sessions started for child tasks.
func denyUnapprovedLinearWatch(ctx context.Context, taskID string, getTask func(context.Context, string) (*models.Task, error)) error {
	return hierarchy.DenyLinearWatch(ctx, taskID, getTask)
}
