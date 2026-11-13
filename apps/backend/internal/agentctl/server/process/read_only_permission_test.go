package process

import (
	"context"
	"testing"

	"github.com/kandev/kandev/internal/agentctl/acpcompat"
	"github.com/kandev/kandev/internal/agentctl/server/adapter"
	"github.com/kandev/kandev/internal/agentctl/server/config"
	"github.com/kandev/kandev/internal/agentctl/types/streams"
	p "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/kandev/kandev/pkg/agent"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyPermissionRejectsNativeApproval(t *testing.T) {
	mgr := newTestManager(t, t.TempDir())
	mgr.cfg.McpProfile = &p.Context{ReadOnly: true}
	mgr.cfg.AutoApprovePermissions = true
	response, err := mgr.handlePermissionRequest(context.Background(), &adapter.PermissionRequest{
		Options: []adapter.PermissionOption{{OptionID: "allow", Kind: streams.PermissionOptionKindAllowOnce}},
	})
	require.NoError(t, err)
	require.True(t, response.Cancelled)
}

func TestClaudeReadOnlyAdapterPolicyAndPermissionRefusal(t *testing.T) {
	mgr := newTestManager(t, t.TempDir())
	mgr.cfg.AgentType = "claude-acp"
	mgr.cfg.Protocol = agent.ProtocolACP
	mgr.cfg.McpProfile = &p.Context{ReadOnly: true}
	mgr.cfg.AutoApprovePermissions = true
	require.NoError(t, mgr.buildAdapterConfig())
	t.Cleanup(func() { _ = mgr.adapter.Close() })
	require.True(t, mgr.adapterCfg.ToSharedConfig().ReadOnly)
	for _, name := range []string{"Write", "Edit", "Bash", "NotebookEdit", "Task", "WebFetch"} {
		response, err := mgr.handlePermissionRequest(t.Context(), &adapter.PermissionRequest{
			ToolName: &name,
			Options: []adapter.PermissionOption{
				{OptionID: "allow", Kind: streams.PermissionOptionKindAllowOnce},
				{OptionID: "reject", Kind: streams.PermissionOptionKindRejectOnce},
			},
		})
		require.NoError(t, err)
		require.Equal(t, &adapter.PermissionResponse{OptionID: "reject"}, response, name)
	}
}

// codexDecisionOption mirrors an option built by the Codex app-server
// transport from availableDecisions.
func codexDecisionOption(id string, kind streams.PermissionOptionKind, decision string) adapter.PermissionOption {
	return adapter.PermissionOption{
		OptionID: id,
		Kind:     kind,
		Metadata: map[string]interface{}{"codex_app_server": true, "codex_decision": decision},
	}
}

func TestReadOnlyPermissionDeclinesCodexRequestWithoutCancellingTurn(t *testing.T) {
	mgr := newTestManager(t, t.TempDir())
	mgr.cfg.McpProfile = &p.Context{ReadOnly: true}
	mgr.cfg.AutoApprovePermissions = true
	// "Cancel turn" is listed before "Deny" with the same kind, so a
	// kind-only pick would abort the turn.
	response, err := mgr.handlePermissionRequest(context.Background(), &adapter.PermissionRequest{
		ActionType: string(streams.ActionTypeFileWrite),
		Options: []adapter.PermissionOption{
			codexDecisionOption("codex-app-server-decision-0", streams.PermissionOptionKindAllowOnce, "accept"),
			codexDecisionOption("codex-app-server-decision-1", streams.PermissionOptionKindRejectOnce, "cancel"),
			codexDecisionOption("codex-app-server-decision-2", streams.PermissionOptionKindRejectOnce, "decline"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, &adapter.PermissionResponse{OptionID: "codex-app-server-decision-2"}, response)

	response, err = mgr.handlePermissionRequest(context.Background(), &adapter.PermissionRequest{
		Options: []adapter.PermissionOption{
			codexDecisionOption("codex-app-server-decision-0", streams.PermissionOptionKindAllowOnce, "accept"),
			codexDecisionOption("codex-app-server-decision-1", streams.PermissionOptionKindRejectOnce, "cancel"),
			codexDecisionOption("codex-app-server-decision-2", streams.PermissionOptionKindRejectAlways, "apply_network_policy_deny"),
		},
	})
	require.NoError(t, err)
	require.Equal(t, &adapter.PermissionResponse{Cancelled: true}, response)
}

func TestReadOnlyPermissionKeepsInjectedKandevMCPApproval(t *testing.T) {
	toolName := "mcp__kandev__update_task_plan_kandev"
	mgr := &Manager{
		cfg: &config.InstanceConfig{
			Port:              43210,
			InjectedKandevMCP: true,
			McpServers:        injectedKandevMCPServers(43210),
			McpProfile:        &p.Context{ReadOnly: true},
		},
		logger: newTestLogger(t),
	}
	response, err := mgr.handlePermissionRequest(context.Background(), &adapter.PermissionRequest{
		ToolName: &toolName,
		Options: []adapter.PermissionOption{
			{OptionID: "reject", Kind: streams.PermissionOptionKindRejectOnce},
			{OptionID: "allow", Kind: streams.PermissionOptionKindAllowOnce},
		},
	})
	require.NoError(t, err)
	require.Equal(t, &adapter.PermissionResponse{OptionID: "allow"}, response)
}

func TestReadOnlyPermissionLeavesHumanInputToThePerson(t *testing.T) {
	mgr := antigravityPermissionManager(t, &config.InstanceConfig{
		AgentType:  acpcompat.AntigravityAgentID,
		McpProfile: &p.Context{ReadOnly: true},
	})
	resultCh := startPermissionCall(mgr, t.Context(), &adapter.PermissionRequest{
		SessionID:  "session-1",
		ToolCallID: acpcompat.AntigravityHumanInputPrefix + "question-1",
		PendingID:  "pending-human",
		Options: []adapter.PermissionOption{
			{OptionID: "option-a", Kind: streams.PermissionOptionKindAllowOnce},
			{OptionID: "no", Kind: streams.PermissionOptionKindRejectOnce},
		},
	})
	event := waitPermissionEvent(t, mgr)
	require.True(t, event.PermissionHumanInput)
	select {
	case result := <-resultCh:
		t.Fatalf("human question was answered by the read-only policy: %+v", result.response)
	default:
	}
	require.NoError(t, mgr.RespondToPermission(event.PendingID, "option-a", false))
	result := waitPermissionCall(t, resultCh)
	require.NoError(t, result.err)
	require.Equal(t, "option-a", result.response.OptionID)
}

func TestReadOnlyRejectsExistingExternalMCP(t *testing.T) {
	profile := p.New(p.SurfaceKanbanTask, nil, nil)
	profile.ReadOnly = true
	profile.BuiltinPackages = &p.BuiltinManifest{Version: 1, CatalogVersion: 1, WorkspaceID: "workspace", TaskID: "task", Surface: profile.Surface, Packages: []p.PackageID{p.PackageCore}, Origins: map[p.PackageID]string{p.PackageCore: "required"}}
	cfg, err := (&config.Config{}).NewInstanceConfig(43210, &config.InstanceOverrides{TaskID: "task"})
	require.NoError(t, err)
	cfg.McpServers = append(cfg.McpServers, config.McpServerConfig{Name: "external", Type: "http", URL: "https://example.test/mcp"})
	mgr := &Manager{cfg: cfg, logger: newTestLogger(t)}
	err = mgr.PrepareBuiltinProfile(profile, func(p.Context) error { t.Fatal("must not bind"); return nil })
	require.ErrorContains(t, err, "external MCP")
}
