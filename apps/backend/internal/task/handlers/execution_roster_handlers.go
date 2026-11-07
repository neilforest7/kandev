package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/kandev/kandev/internal/common/logger"
	"github.com/kandev/kandev/internal/execadmission"
	"github.com/kandev/kandev/internal/task/service"
)

// ExecutionRosterResolver returns the authoritative execution profiles for one
// task. It is the same helper real admission uses, so a roster and a launch
// fingerprint cannot disagree.
type ExecutionRosterResolver func(ctx context.Context, taskID string, profileIDs []string) ([]execadmission.Profile, error)

const maxExecutionRosterProfiles = 8

// RegisterExecutionRosterRoute exposes the read-only roster the controller needs
// before it can register a plan. It is a plain read on the authenticated router:
// the task must belong to the workspace and the profile list is an explicit,
// bounded set of native ids.
func RegisterExecutionRosterRoute(router *gin.Engine, svc *service.Service, resolve ExecutionRosterResolver, log *logger.Logger) {
	if resolve == nil {
		return
	}
	router.GET("/api/v1/workspaces/:id/execution-roster", func(c *gin.Context) {
		workspaceID := c.Param("id")
		taskID := strings.TrimSpace(c.Query("taskId"))
		if workspaceID == "" || taskID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "workspace and task are required"})
			return
		}
		profileIDs, ok := executionRosterProfileIDs(c.Query("profileIds"))
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "between 1 and 8 unique profile ids are required"})
			return
		}
		task, err := svc.GetTask(c.Request.Context(), taskID)
		if err != nil || task == nil || task.WorkspaceID != workspaceID {
			c.JSON(http.StatusNotFound, gin.H{"error": "task is not in this workspace"})
			return
		}
		roster, err := resolve(c.Request.Context(), taskID, profileIDs)
		if err != nil {
			log.Warn("execution roster is unavailable", zap.String("task_id", taskID), zap.Error(err))
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "execution roster is unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profiles": roster})
	})
}

// executionRosterProfileIDs parses the bounded, unique profile id list.
func executionRosterProfileIDs(raw string) ([]string, bool) {
	seen := make(map[string]bool, maxExecutionRosterProfiles)
	ids := make([]string, 0, maxExecutionRosterProfiles)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > maxExecutionRosterProfiles {
		return nil, false
	}
	return ids, true
}
