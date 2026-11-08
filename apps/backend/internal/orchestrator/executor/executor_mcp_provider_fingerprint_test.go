package executor

import (
	"context"
	"testing"

	"github.com/kandev/kandev/internal/agent/mcpconfig"
	"github.com/kandev/kandev/internal/execadmission"
	p "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/kandev/kandev/internal/task/models"
	"github.com/stretchr/testify/require"
)

func providerFingerprint(t *testing.T, context p.Context) string {
	t.Helper()
	grants := &execadmission.MCPGrants{
		Surface:   string(context.Surface),
		ReadOnly:  context.ReadOnly,
		Providers: context.Providers,
	}
	for _, capability := range context.Capabilities {
		grants.Capabilities = append(grants.Capabilities, string(capability))
	}
	if context.BuiltinPackages != nil {
		for _, pkg := range context.BuiltinPackages.Packages {
			grants.Packages = append(grants.Packages, string(pkg))
		}
	}
	profile, err := (execadmission.Resolved{
		ProfileID: "profile-ro", Agent: "claude", Model: "gpt-6.1-terra", Mode: "read-only",
		Runtime: "standalone", MCP: grants,
	}).EffectiveProfile()
	require.NoError(t, err)
	return profile.ConfigDigest
}

// A prepared roster context must carry the same repository providers the launch
// path attaches, because the provider union is part of the approved digest.
func TestPreparedMCPContextCarriesTaskRepositoryProviders(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	repo.tasks["task"] = &models.Task{ID: "task", WorkspaceID: "workspace"}
	repo.taskRepositories["task-repo"] = &models.TaskRepository{ID: "task-repo", TaskID: "task", RepositoryID: "repo"}
	repo.repositories["repo"] = &models.Repository{ID: "repo", Provider: "github"}
	session := &models.TaskSession{ID: "session", TaskID: "task", AgentProfileID: "profile-ro"}
	manager := &mockAgentManager{resolveAgentProfileFunc: func(context.Context, string) (*AgentProfileInfo, error) {
		return &AgentProfileInfo{ProfileID: "profile-ro", AgentName: "claude", Model: "gpt-6.1-terra", Mode: "read-only"}, nil
	}}
	exec := newTestExecutor(t, manager, repo)
	exec.SetBuiltinPolicyReader(&builtinSnapshotReader{snapshot: &mcpconfig.BuiltinPolicySnapshot{
		Policy:      mcpconfig.DefaultBuiltinWorkspacePolicy("workspace"),
		Preferences: &mcpconfig.BuiltinTaskPreferences{Version: 1, WorkspaceID: "workspace", TaskID: "task"},
	}})

	prepared, err := exec.PrepareTaskSessionMCPProfile(ctx, "task", session, false)
	require.NoError(t, err)
	require.Equal(t, []string{"github"}, prepared.Providers)

	// The launch path derives the same union from its resolved repositories.
	launch := prepared
	allRepos := []*repoInfo{{Repository: &models.Repository{ID: "repo", Provider: "github"}}}
	launch.Providers = deriveMCPProviders(allRepos)
	require.Equal(t, launch.Providers, prepared.Providers)
	require.Equal(t, providerFingerprint(t, launch), providerFingerprint(t, prepared))

	// A different repository provider must change the approved fingerprint.
	repo.repositories["repo"] = &models.Repository{ID: "repo", Provider: "gitlab"}
	changed, err := exec.PrepareTaskSessionMCPProfile(ctx, "task", session, false)
	require.NoError(t, err)
	require.Equal(t, []string{"gitlab"}, changed.Providers)
	require.NotEqual(t, providerFingerprint(t, prepared), providerFingerprint(t, changed))
}

// A repository row that cannot be resolved refuses instead of shrinking the
// provider union (a silently smaller union would change the approved digest).
func TestPreparedMCPContextRefusesAnUnavailableProviderRepository(t *testing.T) {
	ctx := context.Background()
	repo := newMockRepository()
	repo.tasks["task"] = &models.Task{ID: "task", WorkspaceID: "workspace"}
	repo.taskRepositories["task-repo"] = &models.TaskRepository{ID: "task-repo", TaskID: "task", RepositoryID: "missing"}
	exec := newTestExecutor(t, &mockAgentManager{}, repo)
	_, err := exec.PrepareTaskSessionMCPProfile(ctx, "task", &models.TaskSession{ID: "session", TaskID: "task", AgentProfileID: "p"}, false)
	require.Error(t, err)
}
