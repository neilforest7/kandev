package lifecycle

import (
	"context"
	"testing"

	p "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/stretchr/testify/require"
)

type readOnlyProfileResolver struct{ mode string }

func (r *readOnlyProfileResolver) ResolveProfile(_ context.Context, id string) (*AgentProfileInfo, error) {
	return &AgentProfileInfo{ProfileID: id, AgentName: "codex-acp", Mode: r.mode}, nil
}

func TestReadOnlyBindingRejectsProfileChangesAndUnprovenHelper(t *testing.T) {
	mgr := newTestManager(t)
	resolver := &readOnlyProfileResolver{mode: "read-only"}
	mgr.profileResolver = resolver
	execution := &AgentExecution{AgentProfileID: "profile"}
	require.Error(t, mgr.verifyReadOnlyMCPProfile(context.Background(), execution))
	profile := builtinDispatchProfile(p.PackageCore)
	profile.ReadOnly = true
	encoded, err := p.MarshalBuiltinBinding(profile)
	require.NoError(t, err)
	execution.setMetadataValue(p.BuiltinBindingMetadataKey, encoded)
	require.NoError(t, mgr.verifyReadOnlyMCPProfile(context.Background(), execution))
	resolver.mode = "agent"
	require.Error(t, mgr.verifyReadOnlyMCPProfile(context.Background(), execution))
	profile.ReadOnly = false
	encoded, err = p.MarshalBuiltinBinding(profile)
	require.NoError(t, err)
	execution.setMetadataValue(p.BuiltinBindingMetadataKey, encoded)
	profile.ReadOnly = true
	require.ErrorContains(t, mgr.prepareBuiltinWorkspaceExecution(context.Background(), execution, &LaunchRequest{McpProfile: &profile}), "read-only")
}

func TestReadOnlyRejectsPassthroughAndExternalMCP(t *testing.T) {
	mgr := newTestManager(t)
	mgr.profileResolver = &readOnlyProfileResolver{mode: "read-only"}
	_, err := mgr.Launch(context.Background(), &LaunchRequest{AgentProfileID: "profile", IsPassthrough: true})
	require.ErrorContains(t, err, "passthrough")
	require.Error(t, validateReadOnlyMCPServers(true, 1))
	require.NoError(t, validateReadOnlyMCPServers(true, 0))
	require.NoError(t, validateReadOnlyMCPServers(false, 1))
}
