package process

import (
	"context"
	"testing"

	"github.com/kandev/kandev/internal/agentctl/server/adapter"
	"github.com/kandev/kandev/internal/agentctl/server/config"
	"github.com/kandev/kandev/internal/agentctl/types/streams"
	p "github.com/kandev/kandev/internal/mcp/profile"
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
