package executor

import (
	"context"
	"testing"

	"github.com/kandev/kandev/internal/agent/mcpconfig"
	"github.com/kandev/kandev/internal/task/models"
	"github.com/stretchr/testify/require"
)

// A read-only worktree preparation runs with no agent start. It must still carry
// the native bound built-in policy, because the runtime refuses a read-only
// launch whose profile is unbound.
func TestReadOnlyWorkspacePreparationBindsTheNativePolicy(t *testing.T) {
	repo := newMockRepository()
	repo.tasks["task"] = &models.Task{ID: "task", WorkspaceID: "workspace"}
	session := &models.TaskSession{ID: "session", TaskID: "task", AgentProfileID: "profile-ro"}
	manager := &mockAgentManager{resolveAgentProfileFunc: func(context.Context, string) (*AgentProfileInfo, error) {
		return &AgentProfileInfo{ProfileID: "profile-ro", AgentName: "claude", Mode: "read-only"}, nil
	}}
	exec := newTestExecutor(t, manager, repo)
	reader := &builtinSnapshotReader{snapshot: &mcpconfig.BuiltinPolicySnapshot{
		Policy:      mcpconfig.DefaultBuiltinWorkspacePolicy("workspace"),
		Preferences: &mcpconfig.BuiltinTaskPreferences{Version: 1, WorkspaceID: "workspace", TaskID: "task"},
	}}
	exec.SetBuiltinPolicyReader(reader)

	profile, err := exec.launchMCPProfile(context.Background(), "task", session, false)
	require.NoError(t, err)
	require.True(t, profile.ReadOnly)
	require.NotNil(t, profile.BuiltinPackages, "workspace preparation must bind the native policy")
	require.NotEmpty(t, profile.BuiltinPackages.Packages)
	require.Equal(t, 1, reader.reads, "the native policy snapshot is read once")

	// The negative guard is unchanged: an unbound read-only profile is still
	// rejected rather than silently allowed.
	unbound, err := exec.resolveTaskSessionMCPProfile(context.Background(), "task", session, false)
	require.NoError(t, err)
	require.Nil(t, unbound.BuiltinPackages, "the raw resolution stays unbound; the runtime guard still rejects it")
}
