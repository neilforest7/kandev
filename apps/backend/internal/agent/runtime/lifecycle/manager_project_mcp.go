package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/kandev/kandev/internal/agent/agents"
	"github.com/kandev/kandev/internal/agent/mcpconfig"
	agentctltypes "github.com/kandev/kandev/internal/agentctl/types"
	"github.com/kandev/kandev/internal/agentruntime"
)

// materializeRuntimeProjectMCP writes project-local MCP config for protocol-mode
// agents whose underlying CLI does not consume ACP session/new mcpServers.
func (m *Manager) materializeRuntimeProjectMCP(
	ctx context.Context,
	execution *AgentExecution,
	agentConfig agents.Agent,
	profileInfo *AgentProfileInfo,
	executorType string,
) error {
	return m.materializeRuntimeProjectMCPWithPreparation(ctx, execution, agentConfig, profileInfo, executorType, nil)
}

func (m *Manager) materializeRuntimeProjectMCPWithPreparation(
	ctx context.Context,
	execution *AgentExecution,
	agentConfig agents.Agent,
	profileInfo *AgentProfileInfo,
	executorType string,
	progress *prepareProgressRecorder,
	operations ...*preparedMCPOperation,
) error {
	if execution == nil || agentConfig == nil {
		return nil
	}
	rt := agentConfig.Runtime()
	if rt == nil || rt.ProjectMCPStrategy == nil {
		return nil
	}
	if isCursorMCPAuthStrategy(rt.ProjectMCPStrategy) {
		if err := m.prepareCursorMCPAuthWithProgress(execution, profileInfo, executorType, rt.ProjectMCPStrategy, progress); err != nil {
			return err
		}
		return m.reconcileAndMaterializeCursorProjectMCPWithPreparation(ctx, execution, agentConfig, profileInfo, executorType, rt.ProjectMCPStrategy, progress, operations...)
	}
	if err := m.prepareCursorMCPAuth(execution, profileInfo, executorType, rt.ProjectMCPStrategy); err != nil {
		return err
	}
	servers, err := m.runtimeProjectMCPServers(ctx, execution, agentConfig, profileInfo, executorType, rt.ProjectMCPStrategy, operations...)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}
	artifacts, err := rt.ProjectMCPStrategy.BuildPassthroughMCP(servers, m.passthroughMCPPaths(execution))
	if err != nil {
		return fmt.Errorf("build project MCP config: %w", err)
	}
	if err := m.writePassthroughMCPFiles(ctx, execution, artifacts.Files); err != nil {
		return err
	}
	if _, ok := rt.ProjectMCPStrategy.(mcpconfig.PiStrategy); ok {
		return ensurePiProjectTrust(ctx, execution)
	}
	return nil
}

// Pi RPC must trust this workspace to load its host-generated project MCP file.
func ensurePiProjectTrust(ctx context.Context, execution *AgentExecution) error {
	if execution == nil || execution.RuntimeName != agentruntime.RuntimeStandalone {
		return fmt.Errorf("pi project trust requires a standalone execution")
	}
	env := execution.RuntimeEnvironment()
	agentDir := ""
	if env != nil {
		agentDir = strings.TrimSpace(env["PI_CODING_AGENT_DIR"])
		if agentDir == "" {
			if home := strings.TrimSpace(env["HOME"]); home != "" {
				agentDir = filepath.Join(home, ".pi", "agent")
			}
		}
	}
	if agentDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("pi project trust: resolve home: %w", err)
		}
		agentDir = filepath.Join(home, ".pi", "agent")
	}
	workspace := strings.TrimSpace(execution.WorkspacePath)
	if workspace == "" {
		return fmt.Errorf("pi project trust: workspace path is required")
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return fmt.Errorf("pi project trust: resolve workspace: %w", err)
	}
	canonicalWorkspace, err = filepath.Abs(canonicalWorkspace)
	if err != nil {
		return fmt.Errorf("pi project trust: absolute workspace: %w", err)
	}
	if !filepath.IsAbs(agentDir) {
		return fmt.Errorf("pi project trust requires an absolute agent directory")
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return fmt.Errorf("pi project trust: create agent directory: %w", err)
	}
	trustPath := filepath.Join(agentDir, "trust.json")
	lockFile, err := acquirePassthroughMCPFileLock(ctx, trustPath)
	if err != nil {
		return fmt.Errorf("pi project trust: lock: %w", err)
	}
	defer func() { _ = releasePassthroughMCPFileLock(lockFile) }()
	trusted := map[string]any{}
	if raw, readErr := os.ReadFile(trustPath); readErr == nil {
		if err := json.Unmarshal(raw, &trusted); err != nil {
			return fmt.Errorf("pi project trust: parse existing store: %w", err)
		}
		if trusted == nil {
			return fmt.Errorf("pi project trust: invalid existing store")
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("pi project trust: read existing store: %w", readErr)
	}
	for key, value := range trusted {
		if value != true && value != false && value != nil {
			return fmt.Errorf("pi project trust: invalid existing entry %q", key)
		}
	}
	trusted[canonicalWorkspace] = true
	content, err := json.MarshalIndent(trusted, "", "  ")
	if err != nil {
		return fmt.Errorf("pi project trust: encode store: %w", err)
	}
	content = append(content, '\n')
	return writeAtomicFile(trustPath, content, 0o600)
}

func (m *Manager) runtimeProjectMCPServers(
	ctx context.Context,
	execution *AgentExecution,
	agentConfig agents.Agent,
	profileInfo *AgentProfileInfo,
	executorType string,
	strategy mcpconfig.PassthroughMCPStrategy,
	operations ...*preparedMCPOperation,
) ([]agentctltypes.McpServer, error) {
	servers, err := m.passthroughMCPServers(ctx, execution, agentConfig, profileInfo, executorType, strategy, operations...)
	if err == nil {
		return servers, nil
	}
	if passthroughMCPConfigPort(execution) <= 0 {
		m.logger.Warn("skipping project MCP config: agentctl instance port unavailable",
			zap.String("execution_id", execution.ID),
			zap.String("agent_id", agentConfig.ID()))
		return nil, nil
	}
	return nil, err
}
