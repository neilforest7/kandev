package mcp

import (
	"context"
	"testing"

	p "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyProfileRestrictsToolsAndCannotBeWidened(t *testing.T) {
	profile := p.Legacy(ModeTask, false, nil)
	s := NewWithProfile(&testBackend{}, "session", "task", 10005, newTestLogger(t), "", false, profile)
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	write := s.mcpServer.ListTools()["update_task_kandev"]
	require.NotNil(t, write)
	profile.ReadOnly = true
	s.SetProfile(profile)
	tools := s.mcpServer.ListTools()
	require.Len(t, tools, 6)
	for _, name := range []string{"get_task_plan_kandev", "get_task_conversation_kandev", "ask_user_question_kandev", "create_task_plan_kandev", "update_task_plan_kandev", "edit_task_plan_kandev"} {
		require.Contains(t, tools, name)
	}
	other := mcp.CallToolRequest{}
	other.Params.Arguments = map[string]any{"task_id": "other-task", "content": "plan"}
	result, err := tools["create_task_plan_kandev"].Handler(context.Background(), other)
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "only its own task plan")
	result, err = write.Handler(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.True(t, result.IsError)
	profile.ReadOnly = false
	s.SetProfile(profile)
	s.SetMode(ModeConfig)
	require.True(t, s.Profile().ReadOnly)
	for name := range s.mcpServer.ListTools() {
		require.True(t, readOnlyToolAllowed(name), name)
	}
	unrestricted := packageTestProfile(p.PackageCore)
	require.Error(t, s.BindInitialProfile(unrestricted, 10005))
}

func TestReadOnlyBuiltinPackagesDoNotExposeWrites(t *testing.T) {
	profile := packageTestProfile(p.PackageCore, p.PackageConfiguration)
	profile.ReadOnly = true
	s := NewWithProfile(&testBackend{}, "session", "task", 10005, newTestLogger(t), "", false, profile)
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	require.Len(t, s.mcpServer.ListTools(), 6)
	require.Empty(t, s.builtinPackages[p.PackageConfiguration].registry.ListTools())
}

func TestExternalSurfaceReadsTaskPlansOnly(t *testing.T) {
	s := NewWithProfile(&testBackend{}, "", "", 10005, newTestLogger(t), "", false, p.Legacy(ModeExternal, true, nil))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	tools := s.mcpServer.ListTools()
	require.Contains(t, tools, "get_task_plan_kandev")
	for _, name := range []string{"create_task_plan_kandev", "update_task_plan_kandev", "edit_task_plan_kandev", "delete_task_plan_kandev"} {
		require.NotContains(t, tools, name)
	}
}
