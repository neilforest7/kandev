package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.uber.org/zap"

	mcpprofile "github.com/kandev/kandev/internal/mcp/profile"

	"github.com/kandev/kandev/internal/execadmission"
	"github.com/kandev/kandev/internal/task/models"
)

// MetadataKeyExecutionAdmissionProfile records the final profile an execution
// was admitted with, so prompt, terminal and recovery gates re-check the exact
// approved configuration instead of trusting live mutable state.
const MetadataKeyExecutionAdmissionProfile = "execution_admission_profile"

// SetExecutionAdmission installs the trusted Linear execution gate. It is
// always wired: a watch execution stays denied whenever the task service cannot
// prove an approval, including when no bridge is configured.
func (m *Manager) SetExecutionAdmission(admit func(context.Context, execadmission.Candidate) error) {
	m.executionAdmission = admit
	if m.sessionManager != nil {
		m.sessionManager.executionAdmission = admit
		m.sessionManager.admitExecution = m.admitRecoveredExecution
	}
}

// SetExecutionScope installs the native scope answer the gate asks before it
// resolves any profile: only work under a trusted Linear watch root is gated.
// Otherwise every execution would need a concrete profile, which base-model
// profiles do not have.
func (m *Manager) SetExecutionScope(scope func(context.Context, string, string) (bool, error)) {
	m.executionScope = scope
	if m.sessionManager != nil {
		m.sessionManager.executionScope = scope
	}
}

// executionInScope reports whether the execution is subject to the trusted
// gate. It asks with both native identities: a watch task launched through a
// session-less path must still be in scope, and its admission then fails on the
// missing session instead of bypassing the gate.
func (m *Manager) executionInScope(ctx context.Context, taskID, sessionID string) (bool, error) {
	if m.executionAdmission == nil {
		return false, nil
	}
	if m.executionScope == nil {
		return true, nil
	}
	return m.executionScope(ctx, taskID, sessionID)
}

// executionProfile builds the final profile from the resolved launch
// configuration. The digest covers the profile's own configuration, which is
// exactly what a pre-launch roster can compute, so the same helper serves both
// the roster snapshot and real admission.
func executionProfile(agentType string, profile *AgentProfileInfo, runtimeName string, mcp *mcpprofile.Context) (execadmission.Profile, error) {
	if profile == nil {
		return execadmission.Profile{}, errors.New("execution profile is unresolved")
	}
	resolved := execadmission.Resolved{
		ProfileID:                  profile.ProfileID,
		Agent:                      agentType,
		Model:                      profile.Model,
		Mode:                       profile.Mode,
		Runtime:                    strings.TrimSpace(runtimeName),
		CLIFlags:                   profile.CLIFlags,
		CommandPrefix:              profile.CommandPrefix,
		ConfigOptions:              profile.ConfigOptions,
		AutoApprove:                profile.AutoApprove,
		DangerouslySkipPermissions: profile.DangerouslySkipPermissions,
		AutoFallback:               profile.AutoFallback,
		RequireExactModel:          profile.RequireExactModel,
		CLIPassthrough:             profile.CLIPassthrough,

		MCPSelectionMode:   profile.MCPSelectionMode,
		MCPSelectedServers: profile.MCPSelectedServers,
		MCPSelectedPlugins: profile.CopilotSelectedPlugins,
		MCP:                admissionMCPGrants(mcp),

		ProviderKind:           profile.ProviderKind,
		ProviderBaseURL:        profile.ProviderBaseURL,
		ProviderAPIKeySecretID: profile.ProviderAPIKeySecretID,
	}
	if profile.ProfileID == "" {
		resolved.ProfileID = profile.AgentName
	}
	return resolved.EffectiveProfile()
}

// admissionDenialReason reports only a typed, documented protocol reason. Any
// other failure is reported as a fixed code so a transport URL or an unexpected
// error value can never reach the log.
func admissionDenialReason(denial error) string {
	var denied *execadmission.Denied
	if errors.As(denial, &denied) && denied != nil && denied.Reason != "" {
		return denied.Reason
	}
	return "admission-failed"
}

// logExecutionAdmissionDenial records why an execution was denied with only the
// bounded identity and privilege dimensions an operator needs. Providers are
// counted, never printed, and no URL, option value, environment, secret or
// request body is included.
func (m *Manager) logExecutionAdmissionDenial(req *LaunchRequest, profile execadmission.Profile, runtimeName string, denial error) {
	if m == nil || m.logger == nil || req == nil {
		return
	}
	fields := []zap.Field{
		zap.String("task_id", req.TaskID),
		zap.String("session_id", req.SessionID),
		zap.String("profile_id", profile.ID),
		zap.String("model", profile.Model),
		zap.String("permission_mode", profile.PermissionMode),
		zap.String("config_digest", profile.ConfigDigest),
		zap.String("runtime", runtimeName),
		zap.String("denial_reason", admissionDenialReason(denial)),
	}
	if mcp := req.McpProfile; mcp != nil {
		capabilities := make([]string, 0, len(mcp.Capabilities))
		for _, capability := range mcp.Capabilities {
			capabilities = append(capabilities, string(capability))
		}
		var packages []string
		if mcp.BuiltinPackages != nil {
			for _, pkg := range mcp.BuiltinPackages.Packages {
				packages = append(packages, string(pkg))
			}
		}
		fields = append(fields,
			zap.String("mcp_surface", string(mcp.Surface)),
			zap.Bool("mcp_read_only", mcp.ReadOnly),
			zap.Strings("mcp_capabilities", capabilities),
			zap.Strings("mcp_packages", packages),
			zap.Int("mcp_provider_count", len(mcp.Providers)),
		)
	}
	m.logger.Warn("execution admission denied", fields...)
}

// admissionMCPGrants reduces a resolved MCP context to the privilege dimensions
// an approval covers. Identity ids stay bound separately.
func admissionMCPGrants(mcp *mcpprofile.Context) *execadmission.MCPGrants {
	if mcp == nil {
		return nil
	}
	grants := &execadmission.MCPGrants{
		Surface:  string(mcp.Surface),
		ReadOnly: mcp.ReadOnly,
	}
	for _, capability := range mcp.Capabilities {
		grants.Capabilities = append(grants.Capabilities, string(capability))
	}
	grants.Providers = append(grants.Providers, mcp.Providers...)
	if mcp.BuiltinPackages != nil {
		for _, pkg := range mcp.BuiltinPackages.Packages {
			grants.Packages = append(grants.Packages, string(pkg))
		}
	}
	// The protected binding stores these sets sorted, so the digest sorts them
	// too: otherwise the same bound context hashes differently after a round trip.
	slices.Sort(grants.Capabilities)
	slices.Sort(grants.Packages)
	return grants
}

// admissionMCPContext parses the protected built-in binding an execution was
// launched with, so a recovery digest covers the same privileges.
func admissionMCPContext(execution *AgentExecution) *mcpprofile.Context {
	value, bound := execution.metadataValue(mcpprofile.BuiltinBindingMetadataKey)
	if !bound {
		return nil
	}
	context, err := mcpprofile.ParseBuiltinBindingMetadata(value)
	if err != nil || context == nil {
		return nil
	}
	return context
}

// admitCandidate runs the gate. A manager without a gate configured leaves the
// pre-existing behavior for non-watch tasks; production always wires it.
func (m *Manager) admitCandidate(ctx context.Context, sessionID, taskID string, profile execadmission.Profile) error {
	if m.executionAdmission == nil {
		return nil
	}
	return m.executionAdmission(ctx, execadmission.Candidate{
		SessionID: sessionID,
		TaskID:    taskID,
		Profile:   profile,
	})
}

// admitExecutionProfile runs the gate for a session that has not been admitted
// yet, then records the admitted profile on the execution.
func (m *Manager) admitExecutionProfile(ctx context.Context, execution *AgentExecution, profile execadmission.Profile) error {
	if execution == nil {
		return errors.New("execution admission requires an execution")
	}
	if err := m.admitCandidate(ctx, execution.SessionID, execution.TaskID, profile); err != nil {
		return err
	}
	execution.setMetadataValue(MetadataKeyExecutionAdmissionProfile, profile)
	return nil
}

// admissionProfileMetadata carries the admitted profile into the execution
// metadata the launch request produces.
func admissionProfileMetadata(metadata map[string]interface{}, profile execadmission.Profile) map[string]interface{} {
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		next[key] = value
	}
	next[MetadataKeyExecutionAdmissionProfile] = profile
	return next
}

// admitResolvedLaunch gates a launch before it has any side effects and returns
// the profile that must be recorded on the execution it creates.
func (m *Manager) admitResolvedLaunch(ctx context.Context, req *LaunchRequest, agentType string, profile *AgentProfileInfo) (execadmission.Profile, error) {
	if m.executionAdmission == nil {
		return execadmission.Profile{}, nil
	}
	inScope, err := m.executionInScope(ctx, req.TaskID, req.SessionID)
	if err != nil {
		return execadmission.Profile{}, err
	}
	if !inScope {
		return execadmission.Profile{}, nil
	}
	effective, err := executionProfile(agentType, profile, string(models.ExecutorType(req.ExecutorType).Runtime()), req.McpProfile)
	if err != nil {
		return execadmission.Profile{}, err
	}
	if err := m.admitCandidate(ctx, req.SessionID, req.TaskID, effective); err != nil {
		m.logExecutionAdmissionDenial(req, effective, string(models.ExecutorType(req.ExecutorType).Runtime()), err)
		return execadmission.Profile{}, err
	}
	return effective, nil
}

// executionAdmissionProfile returns the profile recorded when this execution
// was admitted. Execution metadata survives a JSON round-trip, so the recorded
// structure is parsed back through the same canonical shape.
func executionAdmissionProfile(execution *AgentExecution) (execadmission.Profile, bool) {
	value, ok := execution.metadataValue(MetadataKeyExecutionAdmissionProfile)
	if !ok || value == nil {
		return execadmission.Profile{}, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return execadmission.Profile{}, false
	}
	var profile execadmission.Profile
	if err := json.Unmarshal(encoded, &profile); err != nil {
		return execadmission.Profile{}, false
	}
	if profile.ID == "" || profile.Model == "" || profile.PermissionMode == "" || profile.ConfigDigest == "" {
		return execadmission.Profile{}, false
	}
	return profile, true
}

// freshAdmissionProfile rebuilds the effective profile from current native
// configuration. A changed profile, model, mode, option set or MCP grant no
// longer matches the admitted one.
//
// shortcut: legacy model-only routes apply their override during launch, so
// their recomputed digest can differ here; such a session is denied and needs a
// new session, which is the fail-closed side.
func (m *Manager) freshAdmissionProfile(ctx context.Context, execution *AgentExecution, profileID string) (execadmission.Profile, error) {
	if m.profileResolver == nil || profileID == "" {
		return execadmission.Profile{}, errors.New("execution admission profile is unavailable")
	}
	info, err := m.profileResolver.ResolveProfile(ctx, profileID)
	if err != nil {
		return execadmission.Profile{}, fmt.Errorf("resolve execution admission profile: %w", err)
	}
	return executionProfile(info.AgentName, info, string(execution.RuntimeName), admissionMCPContext(execution))
}

// admitRecordedExecution re-checks an execution that already carries an
// admitted profile. It is the prompt, steer, terminal-input and recovery gate.
func (m *Manager) admitRecordedExecution(ctx context.Context, execution *AgentExecution) error {
	if m.executionAdmission == nil || execution == nil {
		return nil
	}
	inScope, err := m.executionInScope(ctx, execution.TaskID, execution.SessionID)
	if err != nil {
		return err
	}
	if !inScope {
		return nil
	}
	profile, ok := executionAdmissionProfile(execution)
	if !ok {
		return errors.New("execution admission profile is unavailable; a new session is required")
	}
	if err := m.admittedProfileStillHolds(ctx, execution, profile); err != nil {
		return err
	}
	return m.executionAdmission(ctx, execadmission.Candidate{
		SessionID: execution.SessionID,
		TaskID:    execution.TaskID,
		Profile:   profile,
	})
}

// admittedProfileStillHolds denies when the live configuration no longer equals
// the admitted one. The bridge decision is only as good as the configuration it
// was bound to.
func (m *Manager) admittedProfileStillHolds(ctx context.Context, execution *AgentExecution, recorded execadmission.Profile) error {
	fresh, err := m.freshAdmissionProfile(ctx, execution, recorded.ID)
	if err != nil {
		return err
	}
	if fresh != recorded {
		return errors.New("execution profile changed since it was admitted; a new session is required")
	}
	return nil
}

// denyAdmittedProfileChange refuses a live mode or model change on an execution
// that was admitted under a trusted approval: the approval covers the exact
// configuration, so changing it requires a new controlled session.
func (m *Manager) denyAdmittedProfileChange(execution *AgentExecution) error {
	if m.executionAdmission == nil || execution == nil {
		return nil
	}
	if _, ok := executionAdmissionProfile(execution); !ok {
		return nil
	}
	return errors.New("an admitted execution cannot change its mode or model; start a new session")
}

// admitRecoveredExecution re-establishes the admitted profile after a process
// restart, where the in-memory record is gone but the native profile is still
// the one the approval covered.
func (m *Manager) admitRecoveredExecution(ctx context.Context, execution *AgentExecution) error {
	if m.executionAdmission == nil || execution == nil {
		return nil
	}
	inScope, err := m.executionInScope(ctx, execution.TaskID, execution.SessionID)
	if err != nil {
		return err
	}
	if !inScope {
		return nil
	}
	if _, ok := executionAdmissionProfile(execution); ok {
		return m.admitRecordedExecution(ctx, execution)
	}
	profileID := execution.ExecutionProfileID
	if profileID == "" {
		profileID = execution.AgentProfileID
	}
	profile, err := m.freshAdmissionProfile(ctx, execution, profileID)
	if err != nil {
		return err
	}
	return m.admitExecutionProfile(ctx, execution, profile)
}

// ExecutionRoster returns the authoritative profile fingerprints an approval
// roster may name: the same helper real admission uses, from native profile
// configuration only. It reads no environment, secrets or runtime state.
// SetExecutionMCPPolicyResolver installs the native task-session MCP policy the
// roster fingerprints must cover. Without it the roster refuses to answer,
// because a profile-only digest would hide changed workspace MCP grants.
func (m *Manager) SetExecutionMCPPolicyResolver(resolve func(context.Context, string, string) (*mcpprofile.Context, error)) {
	m.executionMCPPolicy = resolve
}

// SetExecutionRuntimeResolver installs the native runtime lookup the roster
// fingerprint needs. Runtime is an approved dimension, so the roster refuses to
// answer without it rather than guess an empty value.
func (m *Manager) SetExecutionRuntimeResolver(resolve func(context.Context, string, string) (string, error)) {
	m.executionRuntime = resolve
}

// ExecutionRoster returns the roster for one task: native profile resolution
// plus the native MCP policy a session of this task would run with.
func (m *Manager) ExecutionRoster(ctx context.Context, taskID string, profileIDs []string) ([]execadmission.Profile, error) {
	if m.profileResolver == nil {
		return nil, errors.New("execution roster profile resolver is unavailable")
	}
	seen := make(map[string]bool, len(profileIDs))
	if m.executionMCPPolicy == nil {
		return nil, errors.New("execution roster MCP policy resolver is unavailable")
	}
	if m.executionRuntime == nil {
		return nil, errors.New("execution roster runtime resolver is unavailable")
	}
	roster := make([]execadmission.Profile, 0, len(profileIDs))
	for _, profileID := range profileIDs {
		if profileID == "" || seen[profileID] {
			continue
		}
		seen[profileID] = true
		info, err := m.profileResolver.ResolveProfile(ctx, profileID)
		if err != nil {
			return nil, fmt.Errorf("resolve execution roster profile %q: %w", profileID, err)
		}
		mcp, err := m.executionMCPPolicy(ctx, taskID, profileID)
		if err != nil {
			return nil, fmt.Errorf("resolve execution roster MCP policy for %q: %w", profileID, err)
		}
		runtimeName, err := m.executionRuntime(ctx, taskID, profileID)
		if err != nil {
			return nil, fmt.Errorf("resolve execution roster runtime for %q: %w", profileID, err)
		}
		profile, err := executionProfile(info.AgentName, info, runtimeName, mcp)
		if err != nil {
			return nil, err
		}
		roster = append(roster, profile)
	}
	if len(roster) == 0 {
		return nil, errors.New("execution roster is empty")
	}
	return roster, nil
}

// ValidateAdmittedExecution re-checks the live configuration of an admitted
// execution against its recorded profile. It is the shared validator the
// admission watchdog uses and it never calls the bridge.
func (m *Manager) ValidateAdmittedExecution(ctx context.Context, sessionID string) error {
	if m.executionAdmission == nil || sessionID == "" {
		return nil
	}
	execution, ok := m.executionStore.GetBySessionID(sessionID)
	if !ok || execution == nil {
		return nil
	}
	inScope, err := m.executionInScope(ctx, execution.TaskID, sessionID)
	if err != nil {
		return err
	}
	if !inScope {
		return nil
	}
	profile, ok := executionAdmissionProfile(execution)
	if !ok {
		return errors.New("execution admission profile is unavailable; a new session is required")
	}
	return m.admittedProfileStillHolds(ctx, execution, profile)
}
