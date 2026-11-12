package api

import (
	"context"
	"testing"

	"github.com/kandev/kandev/internal/agentctl/server/config"
	p "github.com/kandev/kandev/internal/mcp/profile"
	ws "github.com/kandev/kandev/pkg/websocket"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyModeRequiresMatchingProfile(t *testing.T) {
	s := &Server{cfg: &config.InstanceConfig{McpProfile: &p.Context{ReadOnly: true}}}
	require.NoError(t, s.validateReadOnlyModeChange("read-only"))
	require.Error(t, s.validateReadOnlyModeChange("agent"))
	s.cfg.McpProfile.ReadOnly = false
	require.Error(t, s.validateReadOnlyModeChange("read-only"))
	require.NoError(t, s.validateReadOnlyModeChange("agent"))
}

func TestClaudeReadOnlyModeGuard(t *testing.T) {
	s := newTestServer(t)
	native := &readOnlyConfigAdapter{}
	s.procMgr.SetAdapterForTest(native)
	s.cfg.AgentType = "claude-acp"
	s.cfg.McpProfile = &p.Context{ReadOnly: true}
	req, err := ws.NewRequest("plan", "agent.session.set_mode", map[string]string{"mode_id": "plan"})
	require.NoError(t, err)
	require.NotEqual(t, ws.MessageTypeError, s.handleWSSetMode(context.Background(), req).Type)
	require.Equal(t, "plan", native.modeID)
	for _, mode := range []string{"default", "acceptEdits", "auto", "bypassPermissions"} {
		require.Error(t, s.validateReadOnlyModeChange(mode))
	}
	s.cfg.McpProfile.ReadOnly = false
	require.Error(t, s.validateReadOnlyModeChange("plan"))
	require.NoError(t, s.validateReadOnlyModeChange("default"))
}

type readOnlyConfigAdapter struct{ capableAgentAdapter }

func (a *readOnlyConfigAdapter) SetConfigOption(_ context.Context, id, value string) error {
	a.modelID = id + ":" + value
	return nil
}

func TestReadOnlyModeAndConfigRequestsCannotWiden(t *testing.T) {
	s := newTestServer(t)
	s.cfg.McpProfile = &p.Context{ReadOnly: true}
	adapter := &readOnlyConfigAdapter{}
	s.procMgr.SetAdapterForTest(adapter)
	mode, _ := ws.NewRequest("1", "agent.session.set_mode", map[string]string{"mode_id": "agent"})
	require.Equal(t, ws.MessageTypeError, s.handleWSSetMode(context.Background(), mode).Type)
	configReq, _ := ws.NewRequest("2", "agent.session.set_config_option", map[string]string{"config_id": "mode", "value": "agent"})
	require.Equal(t, ws.MessageTypeError, s.handleWSSetConfigOption(context.Background(), configReq).Type)
	require.Empty(t, adapter.modeID)
	require.Empty(t, adapter.modelID)
	mode, _ = ws.NewRequest("3", "agent.session.set_mode", map[string]string{"mode_id": "read-only"})
	require.NotEqual(t, ws.MessageTypeError, s.handleWSSetMode(context.Background(), mode).Type)
	require.Equal(t, "read-only", adapter.modeID)
}

func TestReadOnlyConfigChangesCannotWidenOrReset(t *testing.T) {
	s := newTestServer(t)
	s.cfg.McpProfile = &p.Context{ReadOnly: true}
	adapter := &readOnlyConfigAdapter{}
	s.procMgr.SetAdapterForTest(adapter)

	blocked := []map[string]string{
		{"config_id": "collaboration_mode", "value": "agent"},
		{"config_id": "model", "value": "another-model"},
		{"config_id": "reasoning_effort", "value": " reset "},
		{"config_id": "reasoning_effort", "value": ""},
	}
	for i, payload := range blocked {
		req, _ := ws.NewRequest(string(rune('a'+i)), "agent.session.set_config_option", payload)
		require.Equal(t, ws.MessageTypeError, s.handleWSSetConfigOption(context.Background(), req).Type, "%v", payload)
	}
	require.Empty(t, adapter.modelID)

	allowed, _ := ws.NewRequest("allowed", "agent.session.set_config_option", map[string]string{"config_id": "reasoning_effort", "value": "high"})
	require.NotEqual(t, ws.MessageTypeError, s.handleWSSetConfigOption(context.Background(), allowed).Type)
	require.Equal(t, "reasoning_effort:high", adapter.modelID)
}

func TestReadOnlySessionCannotReset(t *testing.T) {
	s := newTestServer(t)
	s.cfg.McpProfile = &p.Context{ReadOnly: true}
	req, _ := ws.NewRequest("reset", "agent.session.reset", map[string]any{})
	resp := s.handleWSResetSession(context.Background(), req)
	require.Equal(t, ws.MessageTypeError, resp.Type)
}

func TestClaudeReadOnlyEffortConfig(t *testing.T) {
	s := newTestServer(t)
	s.cfg.AgentType = "claude-acp"
	s.cfg.McpProfile = &p.Context{ReadOnly: true}
	native := &readOnlyConfigAdapter{}
	s.procMgr.SetAdapterForTest(native)
	req, _ := ws.NewRequest("effort", "agent.session.set_config_option", map[string]string{"config_id": "effort", "value": "medium"})
	require.NotEqual(t, ws.MessageTypeError, s.handleWSSetConfigOption(t.Context(), req).Type)
	require.Equal(t, "effort:medium", native.modelID)
	for _, id := range []string{"model", "tools", "settingSources", "strictMcpConfig", "allowDangerouslySkipPermissions"} {
		require.Error(t, s.validateReadOnlyConfigChange(id, "medium"))
	}
	for _, value := range []string{"", "default", " reset "} {
		require.Error(t, s.validateReadOnlyConfigChange("effort", value))
	}
	s.cfg.AgentType = "codex-acp"
	require.Error(t, s.validateReadOnlyConfigChange("effort", "medium"))
}
