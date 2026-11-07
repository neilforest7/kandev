// Package execadmission is the host side of the Linear pilot execution
// approval gate. It holds the final-configuration fingerprint, the host-derived
// admission request, the rules the host enforces without the bridge, and a
// fail-closed client for the bridge's loopback check endpoint.
//
// It is a leaf package so the agent lifecycle and the task service share one
// definition of the digest that a human approves.
package execadmission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	settingsmodels "github.com/kandev/kandev/internal/agent/settings/models"
)

// DefaultPermissionMode is reported when the profile requests no explicit ACP
// mode. The bridge needs a stable non-empty value, and an empty mode means the
// agent runs with its own default, so it is never read-only.
const DefaultPermissionMode = "default"

// Intent values accepted by the bridge.
const (
	IntentImplementation = "implementation"
	IntentInvestigation  = "investigation"
)

// BasisInvestigationProof marks an investigation grant. It is the only approval
// an investigation check may return, and it carries no human approval identity
// because it is not a plan approval.
const BasisInvestigationProof = "investigation-proof"

// Profile is the exact effective execution profile an approval is bound to.
// Field names match the bridge contract.
type Profile struct {
	ID             string `json:"id"`
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
	ConfigDigest   string `json:"configDigest"`
}

// MCPGrants is the canonical privilege surface a session runs with: the MCP
// surface, its built-in packages, advertised capabilities, providers and the
// read-only restriction. Identity ids are bound separately and excluded.
type MCPGrants struct {
	Surface      string   `json:"surface,omitempty"`
	Packages     []string `json:"packages,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Providers    []string `json:"providers,omitempty"`
	ReadOnly     bool     `json:"readOnly,omitempty"`
}

// Resolved mirrors the final resolved profile the launch path executes. Every
// dimension that changes model identity or privilege is present so a human
// approves exactly what runs; credentials, env values and workspace paths are
// not, so a relaunch of the same configuration keeps the same digest.
type Resolved struct {
	ProfileID string
	Agent     string
	Model     string
	Mode      string
	Runtime   string

	CLIFlags      []settingsmodels.CLIFlag
	CommandPrefix string
	ConfigOptions map[string]string

	AutoApprove                bool
	DangerouslySkipPermissions bool
	AutoFallback               bool
	RequireExactModel          bool
	CLIPassthrough             bool

	MCPSelectionMode   string
	MCPSelectedServers []string
	MCPSelectedPlugins []string
	MCP                *MCPGrants

	ProviderKind           string
	ProviderBaseURL        string
	ProviderAPIKeySecretID string
}

const fingerprintVersion = "execadmission-fingerprint/v1\n"

// fingerprint is the canonical digest shape. Plain encoding/json sorts map keys,
// so no key is normalised away: a config option the launch path sees stays a
// distinct digest input under its exact key.
type fingerprint struct {
	ProfileID string `json:"profileId"`
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	Mode      string `json:"mode"`
	Runtime   string `json:"runtime"`

	CLIFlags      []string          `json:"cliFlags,omitempty"`
	CommandPrefix string            `json:"commandPrefix,omitempty"`
	ConfigOptions map[string]string `json:"configOptions,omitempty"`

	AutoApprove                bool `json:"autoApprove,omitempty"`
	DangerouslySkipPermissions bool `json:"dangerouslySkipPermissions,omitempty"`
	AutoFallback               bool `json:"autoFallback,omitempty"`
	RequireExactModel          bool `json:"requireExactModel,omitempty"`
	CLIPassthrough             bool `json:"cliPassthrough,omitempty"`

	MCPSelectionMode   string     `json:"mcpSelectionMode,omitempty"`
	MCPSelectedServers []string   `json:"mcpSelectedServers,omitempty"`
	MCPSelectedPlugins []string   `json:"mcpSelectedPlugins,omitempty"`
	MCP                *MCPGrants `json:"mcp,omitempty"`

	ProviderKind           string `json:"providerKind,omitempty"`
	ProviderBaseURL        string `json:"providerBaseUrl,omitempty"`
	ProviderAPIKeySecretID string `json:"providerApiKeySecretId,omitempty"`
}

// validate rejects values whose meaning depends on how a reader trims them. The
// host never "repairs" a configured model or mode into a different digest.
func (r Resolved) validate() error {
	if strings.TrimSpace(r.ProfileID) == "" || r.ProfileID != strings.TrimSpace(r.ProfileID) {
		return errors.New("execution profile identity is unavailable")
	}
	if strings.TrimSpace(r.Agent) == "" || r.Agent != strings.TrimSpace(r.Agent) {
		return errors.New("execution agent identity is unavailable")
	}
	if r.Model != strings.TrimSpace(r.Model) {
		return errors.New("execution profile model is not canonical")
	}
	if r.Mode != strings.TrimSpace(r.Mode) {
		return errors.New("execution profile permission mode is not canonical")
	}
	if r.Model == "" {
		return errors.New("execution profile model is unresolved")
	}
	for key := range r.ConfigOptions {
		if key == "" || key != strings.TrimSpace(key) {
			return errors.New("execution profile config option key is not canonical")
		}
	}
	return nil
}

// Digest is the canonical hash of the final configuration dimensions.
func (r Resolved) Digest() (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	flags := make([]string, 0, len(r.CLIFlags))
	for _, flag := range r.CLIFlags {
		if flag.Enabled && strings.TrimSpace(flag.Flag) != "" {
			flags = append(flags, flag.Flag)
		}
	}
	encoded, err := json.Marshal(fingerprint{
		ProfileID:                  r.ProfileID,
		Agent:                      r.Agent,
		Model:                      r.Model,
		Mode:                       r.Mode,
		Runtime:                    r.Runtime,
		CLIFlags:                   flags,
		CommandPrefix:              r.CommandPrefix,
		ConfigOptions:              r.ConfigOptions,
		AutoApprove:                r.AutoApprove,
		DangerouslySkipPermissions: r.DangerouslySkipPermissions,
		AutoFallback:               r.AutoFallback,
		RequireExactModel:          r.RequireExactModel,
		CLIPassthrough:             r.CLIPassthrough,
		MCPSelectionMode:           r.MCPSelectionMode,
		MCPSelectedServers:         r.MCPSelectedServers,
		MCPSelectedPlugins:         r.MCPSelectedPlugins,
		MCP:                        r.MCP,
		ProviderKind:               r.ProviderKind,
		ProviderBaseURL:            r.ProviderBaseURL,
		ProviderAPIKeySecretID:     r.ProviderAPIKeySecretID,
	})
	if err != nil {
		return "", fmt.Errorf("encode execution fingerprint: %w", err)
	}
	sum := sha256.Sum256(append([]byte(fingerprintVersion), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

// EffectiveProfile resolves the profile reported to the bridge. Its id is the
// agent profile the execution resolved, never a caller-supplied label.
func (r Resolved) EffectiveProfile() (Profile, error) {
	digest, err := r.Digest()
	if err != nil {
		return Profile{}, err
	}
	mode := r.Mode
	if mode == "" {
		mode = DefaultPermissionMode
	}
	return Profile{
		ID:             r.ProfileID,
		Model:          r.Model,
		PermissionMode: mode,
		ConfigDigest:   digest,
	}, nil
}

// Candidate is what the runtime knows before it admits an execution. Intent is
// a host policy decision; the organization, issue, workspace and root task are
// host-derived by the task service from native records, so a caller cannot
// supply them.
type Candidate struct {
	SessionID string
	TaskID    string
	Intent    string
	Profile   Profile
}

// NativeSource is the host-owned Linear association of a watch root.
type NativeSource struct {
	OrganizationID string
	IssueID        string
	WorkspaceID    string
}

// Pin is the write-once admission record kept on the session. It is the only
// source of plan version and profile for later admissions.
type Pin struct {
	ApprovalID  string  `json:"approvalId"`
	PlanVersion string  `json:"planVersion"`
	Intent      string  `json:"intent"`
	Profile     Profile `json:"profile"`
}
