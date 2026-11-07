package lifecycle

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/kandev/kandev/internal/common/logger"
	"github.com/kandev/kandev/internal/execadmission"
	mcpprofile "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// The denial audit must stay diagnosable while never emitting provider values,
// request environment or an unexpected error string such as a transport URL.
func TestExecutionAdmissionDenialAuditOmitsSensitiveValues(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	log, err := logger.NewFromZap(zap.New(core))
	require.NoError(t, err)
	manager := &Manager{logger: log}

	profile := execadmission.Profile{ID: "profile-1", Model: "claude-sonnet-4-6", PermissionMode: "read-only", ConfigDigest: "digest-1"}
	req := &LaunchRequest{
		TaskID: "root-55ce84d9", SessionID: "session-374a8897", ExecutorType: "worktree",
		Env: map[string]string{"SHARED_TOKEN": "env-canary-value"},
		McpProfile: &mcpprofile.Context{
			Surface:         mcpprofile.SurfaceKanbanTask,
			ReadOnly:        true,
			Capabilities:    []mcpprofile.Capability{mcpprofile.CapabilityUserQuestion},
			Providers:       []string{"provider-canary-value"},
			BuiltinPackages: &mcpprofile.BuiltinManifest{Packages: []mcpprofile.PackageID{"core"}},
		},
	}

	manager.logExecutionAdmissionDenial(req, profile, "standalone",
		&execadmission.Denied{Reason: "intent-disabled"})
	plainDenial := errors.New("post https://secret.example/internal/approvals/check?token=canary-secret-value: dial failed")
	manager.logExecutionAdmissionDenial(req, profile, "standalone", plainDenial)

	entries := logs.All()
	require.Len(t, entries, 2)

	typed := entries[0].ContextMap()
	require.Equal(t, "intent-disabled", typed["denial_reason"])
	require.Equal(t, "root-55ce84d9", typed["task_id"])
	require.Equal(t, "session-374a8897", typed["session_id"])
	require.Equal(t, "profile-1", typed["profile_id"])
	require.Equal(t, "claude-sonnet-4-6", typed["model"])
	require.Equal(t, "read-only", typed["permission_mode"])
	require.Equal(t, "digest-1", typed["config_digest"])
	require.Equal(t, "standalone", typed["runtime"])
	require.Equal(t, "kanban-task", typed["mcp_surface"])
	require.Equal(t, true, typed["mcp_read_only"])
	require.Equal(t, int64(1), typed["mcp_provider_count"])

	generic := entries[1].ContextMap()
	require.Equal(t, "admission-failed", generic["denial_reason"])

	envCanary := req.Env["SHARED_TOKEN"]
	providerCanary := req.McpProfile.Providers[0]
	for _, entry := range entries {
		encoded, err := json.Marshal(entry.ContextMap())
		require.NoError(t, err)
		payload := string(encoded)
		require.Contains(t, payload, "user-question", "capability ids stay visible")
		require.Contains(t, payload, "core", "package ids stay visible")
		require.NotContains(t, payload, providerCanary, entry.Message)
		require.NotContains(t, payload, envCanary, entry.Message)
		require.NotContains(t, payload, "canary-secret-value", entry.Message)
		require.NotContains(t, payload, "https://", entry.Message)
		require.NotContains(t, entry.Message, "canary")
	}
}
