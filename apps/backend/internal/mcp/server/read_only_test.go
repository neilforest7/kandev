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
	require.Len(t, tools, 3)
	for _, name := range []string{"get_task_plan_kandev", "get_task_conversation_kandev", "ask_user_question_kandev"} {
		require.Contains(t, tools, name)
	}
	result, err := write.Handler(context.Background(), mcp.CallToolRequest{})
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
	require.Len(t, s.mcpServer.ListTools(), 3)
	require.Empty(t, s.builtinPackages[p.PackageConfiguration].registry.ListTools())
}
