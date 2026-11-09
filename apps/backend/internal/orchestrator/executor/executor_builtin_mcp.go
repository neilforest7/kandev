package executor

import (
	"context"
	"fmt"

	"github.com/kandev/kandev/internal/agent/mcpconfig"
	p "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/kandev/kandev/internal/task/models"
)

type BuiltinPolicyReader interface {
	GetBuiltinPolicySnapshot(context.Context, string, string) (*mcpconfig.BuiltinPolicySnapshot, error)
}

func (e *Executor) SetBuiltinPolicyReader(reader BuiltinPolicyReader) { e.builtinPolicyReader = reader }

func (e *Executor) PrepareTaskSessionMCPProfile(ctx context.Context, taskID string, session *models.TaskSession, title bool) (p.Context, error) {
	if session != nil {
		if prepared := preparedMCPLaunchProfile(ctx, taskID, session.ID); prepared != nil {
			return *prepared, nil
		}
	}
	profile, err := e.resolveTaskSessionMCPProfile(ctx, taskID, session, title)
	if err != nil {
		return profile, err
	}
	if session != nil {
		mode, hasProfile, modeErr := e.sessionProfileMode(ctx, session)
		if modeErr != nil {
			return p.Context{}, modeErr
		}
		if hasProfile {
			profile.ReadOnly = mode == "read-only"
		}
	}
	providers, err := e.resolveTaskMCPProviders(ctx, taskID)
	if err != nil {
		return p.Context{}, err
	}
	profile.Providers = providers
	if e.builtinPolicyReader == nil {
		return profile, nil
	}
	switch profile.Surface {
	case p.SurfaceExternal, p.SurfaceAutomation, p.SurfaceCoordinator, p.SurfaceManagedConversation:
		return profile, nil
	}
	if session == nil || session.TaskID != taskID || session.ID == "" {
		return p.Context{}, fmt.Errorf("built-in MCP preparation requires a matching task session")
	}
	task, err := e.repo.GetTask(ctx, taskID)
	if err != nil {
		return p.Context{}, err
	}
	if task == nil || task.WorkspaceID == "" {
		return p.Context{}, fmt.Errorf("built-in MCP preparation requires a workspace owner")
	}
	snapshot, err := e.builtinPolicySnapshot(ctx, task.WorkspaceID, taskID, profile.Surface)
	if err != nil {
		return p.Context{}, err
	}
	profile.BuiltinPackages, err = mcpconfig.ResolveBuiltinPackages(task.WorkspaceID, taskID, profile.Surface, snapshot.Policy, snapshot.Preferences)
	return profile, err
}

func (e *Executor) builtinPolicySnapshot(ctx context.Context, workspaceID, taskID string, surface p.Surface) (*mcpconfig.BuiltinPolicySnapshot, error) {
	if surface == p.SurfaceConfiguration {
		return &mcpconfig.BuiltinPolicySnapshot{}, nil
	}
	snapshot, err := e.builtinPolicyReader.GetBuiltinPolicySnapshot(ctx, workspaceID, taskID)
	if err != nil {
		return nil, err
	}
	if snapshot == nil || snapshot.Policy == nil || snapshot.Preferences == nil {
		return nil, mcpconfig.ErrBuiltinPolicyInvalid
	}
	return snapshot, nil
}

func (e *Executor) launchMCPProfile(ctx context.Context, taskID string, session *models.TaskSession, startAgent bool) (p.Context, error) {
	return e.PrepareTaskSessionMCPProfile(ctx, taskID, session, startAgent)
}

// sessionProfileMode resolves the session's agent profile mode at the MCP trust
// boundary. A missing resolver, a resolution error or an absent profile with a
// non-empty profile id is a refusal, never a writable fallback. A session with
// no profile id keeps the pre-existing base-profile behavior.
func (e *Executor) sessionProfileMode(ctx context.Context, session *models.TaskSession) (string, bool, error) {
	if session == nil {
		return "", false, nil
	}
	profileID := session.ExecutionProfileID
	if profileID == "" {
		profileID = session.AgentProfileID
	}
	if profileID == "" {
		return "", false, nil
	}
	if e.agentManager == nil {
		return "", true, fmt.Errorf("resolve agent profile %q: agent manager is unavailable", profileID)
	}
	info, err := e.agentManager.ResolveAgentProfile(ctx, profileID)
	if err != nil {
		return "", true, fmt.Errorf("resolve agent profile %q: %w", profileID, err)
	}
	if info == nil {
		return "", true, fmt.Errorf("resolve agent profile %q: profile is unavailable", profileID)
	}
	return info.Mode, true, nil
}

// prepareExistingWorkspaceBuiltin binds the admitted package snapshot before an
// already prepared workspace starts its first agent process.
func (e *Executor) prepareExistingWorkspaceBuiltin(ctx context.Context, executionID string, profile *p.Context) error {
	if profile != nil && profile.ReadOnly && profile.BuiltinPackages == nil {
		return fmt.Errorf("read-only session requires a new workspace with a bound MCP profile")
	}
	if profile == nil || profile.BuiltinPackages == nil {
		return nil
	}
	preparer, ok := e.agentManager.(interface {
		PrepareBuiltinWorkspace(context.Context, string, p.Context) error
	})
	if !ok {
		return fmt.Errorf("agent manager cannot prepare built-in MCP packages")
	}
	return preparer.PrepareBuiltinWorkspace(ctx, executionID, *profile)
}
