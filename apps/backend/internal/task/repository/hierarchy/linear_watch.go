package hierarchy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kandev/kandev/internal/task/models"
)

var ErrLinearWatchApprovalRequired = errors.New("linear watch task requires business approval")

func DenyLinearWatch(ctx context.Context, taskID string, getTask func(context.Context, string) (*models.Task, error)) error {
	seen := map[string]bool{}
	for taskID != "" {
		if seen[taskID] {
			return errors.New("task ancestry is cyclic")
		}
		seen[taskID] = true
		task, err := getTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task == nil {
			return errors.New("task ancestry is unavailable")
		}
		watchID, _ := task.Metadata[models.MetaKeyLinearIssueWatchID].(string)
		if watchID != "" || strings.HasPrefix(task.ExternalID, models.LinearWatchExternalIDPrefix) {
			return ErrLinearWatchApprovalRequired
		}
		taskID = task.ParentID
	}
	return nil
}

func DenyLinearWatchSQL(ctx context.Context, tx *sql.Tx, rebind func(string) string, taskID string) error {
	return DenyLinearWatch(ctx, taskID, func(ctx context.Context, id string) (*models.Task, error) {
		task := &models.Task{ID: id}
		var metadata string
		err := tx.QueryRowContext(ctx, rebind(`SELECT COALESCE(parent_id, ''), COALESCE(external_id, ''), COALESCE(metadata, '{}') FROM tasks WHERE id = ?`), id).Scan(&task.ParentID, &task.ExternalID, &metadata)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metadata), &task.Metadata); err != nil {
			return nil, err
		}
		return task, nil
	})
}
