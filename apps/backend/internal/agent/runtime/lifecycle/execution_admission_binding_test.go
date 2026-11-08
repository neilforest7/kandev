package lifecycle

import (
	"context"
	"slices"
	"testing"

	mcpconfig "github.com/kandev/kandev/internal/agent/mcpconfig"
	agentruntime "github.com/kandev/kandev/internal/agentruntime"
	"github.com/kandev/kandev/internal/execadmission"
	mcpprofile "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/stretchr/testify/require"
)

// A bound context that round-trips through the protected binding keeps the same
// admission digest, and a genuinely different bound context still denies.
func TestAdmissionDigestSurvivesTheProtectedBindingRoundTrip(t *testing.T) {
	manifest, err := mcpconfig.ResolveBuiltinPackages("workspace", "task", mcpprofile.SurfaceKanbanTask,
		mcpconfig.DefaultBuiltinWorkspacePolicy("workspace"), nil)
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Greater(t, len(manifest.Packages), 1)
	originalPackages := slices.Clone(manifest.Packages)

	boundContext := mcpprofile.New(mcpprofile.SurfaceKanbanTask,
		[]mcpprofile.Capability{mcpprofile.CapabilityUserQuestion}, []string{"github"})
	boundContext.ReadOnly = true
	boundContext.BuiltinPackages = manifest
	bound := &boundContext

	info := &AgentProfileInfo{ProfileID: "f8729753-c149-4333-b2e5-8b2c1d3b9576", AgentName: "claude",
		Model: "gpt-6.1-terra", Mode: "read-only"}
	recorded, err := executionProfile(info.AgentName, info, "standalone", bound)
	require.NoError(t, err)
	require.Equal(t, originalPackages, manifest.Packages, "hashing must not mutate the bound context")

	sealed, err := mcpprofile.MarshalBuiltinBinding(*bound)
	require.NoError(t, err)
	parsed, err := mcpprofile.ParseBuiltinBindingMetadata(sealed)
	require.NoError(t, err)
	fresh, err := executionProfile(info.AgentName, info, "standalone", parsed)
	require.NoError(t, err)
	require.Equal(t, recorded, fresh, "the canonical binding reproduces the admission digest")

	manager := &Manager{profileResolver: staticProfileResolver{info: info}}
	manager.SetExecutionAdmission(func(context.Context, execadmission.Candidate) error { return nil })
	execution := &AgentExecution{SessionID: "session-1", TaskID: "task-1", RuntimeName: agentruntime.Runtime("standalone")}
	execution.setMetadataValue(MetadataKeyExecutionAdmissionProfile, recorded)
	execution.setMetadataValue(mcpprofile.BuiltinBindingMetadataKey, sealed)
	require.NoError(t, manager.admitRecordedExecution(context.Background(), execution),
		"the validator accepts the same bound context")

	// Bound drift, recorded profile untouched: a writable binding denies.
	writable := *parsed
	writable.ReadOnly = false
	driftedReadOnly, err := mcpprofile.MarshalBuiltinBinding(writable)
	require.NoError(t, err)
	execution.setMetadataValue(mcpprofile.BuiltinBindingMetadataKey, driftedReadOnly)
	require.Error(t, manager.admitRecordedExecution(context.Background(), execution))

	// Bound drift: a reduced package set denies.
	// Drop one non-core package: the manifest must stay valid for the real seal.
	reduced := *parsed
	reducedManifest := *parsed.BuiltinPackages
	reducedManifest.Packages = slices.Clone(reducedManifest.Packages)
	for index := len(reducedManifest.Packages) - 1; index >= 0; index-- {
		if reducedManifest.Packages[index] != mcpprofile.PackageCore {
			removed := reducedManifest.Packages[index]
			reducedManifest.Packages = append(reducedManifest.Packages[:index], reducedManifest.Packages[index+1:]...)
			if reducedManifest.Origins != nil {
				delete(reducedManifest.Origins, removed)
			}
			break
		}
	}
	reduced.BuiltinPackages = &reducedManifest
	driftedPackages, err := mcpprofile.MarshalBuiltinBinding(reduced)
	require.NoError(t, err)
	execution.setMetadataValue(mcpprofile.BuiltinBindingMetadataKey, driftedPackages)
	require.Error(t, manager.admitRecordedExecution(context.Background(), execution))

	// The original bound context is still accepted once the binding is restored.
	execution.setMetadataValue(mcpprofile.BuiltinBindingMetadataKey, sealed)
	require.NoError(t, manager.admitRecordedExecution(context.Background(), execution))
}
