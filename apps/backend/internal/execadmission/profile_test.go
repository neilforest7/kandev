package execadmission

import (
	"testing"

	settingsmodels "github.com/kandev/kandev/internal/agent/settings/models"
	"github.com/kandev/kandev/internal/task/models"
	v1 "github.com/kandev/kandev/pkg/api/v1"
	"github.com/stretchr/testify/require"
)

func resolved() Resolved {
	return Resolved{
		ProfileID: "f8729753-c149-4333-b2e5-8b2c1d3b9576",
		Agent:     "claude",
		Model:     "claude-sonnet-4-6",
		Mode:      "read-only",
		Runtime:   "local",
		CLIFlags: []settingsmodels.CLIFlag{
			{Flag: "--add-dir /shared", Enabled: true},
			{Flag: "--dangerously-skip-permissions", Enabled: false},
		},
		ConfigOptions: map[string]string{"reasoning_effort": "high"},
		MCP:           &MCPGrants{Surface: "task", Packages: []string{"kandev"}, Providers: []string{"kandev"}, ReadOnly: true},
	}
}

func TestDigestIsStableAndCoversPrivilegeDimensions(t *testing.T) {
	base := resolved()
	digest, err := base.Digest()
	require.NoError(t, err)
	require.Regexp(t, "^[0-9a-f]{64}$", digest)

	same := resolved()
	same.ConfigOptions = map[string]string{"reasoning_effort": "high"}
	sameDigest, err := same.Digest()
	require.NoError(t, err)
	require.Equal(t, digest, sameDigest)

	for name, mutate := range map[string]func(*Resolved){
		"model":            func(r *Resolved) { r.Model = "claude-opus-4-6" },
		"mode":             func(r *Resolved) { r.Mode = "agent" },
		"profile":          func(r *Resolved) { r.ProfileID = "another" },
		"flag":             func(r *Resolved) { r.CLIFlags[1].Enabled = true },
		"config option":    func(r *Resolved) { r.ConfigOptions["reasoning_effort"] = "low" },
		"config key":       func(r *Resolved) { r.ConfigOptions["sandbox"] = "false" },
		"mcp packages":     func(r *Resolved) { r.MCP = &MCPGrants{Surface: "task", Packages: []string{"other"}} },
		"mcp read-only":    func(r *Resolved) { r.MCP = &MCPGrants{Surface: "task", ReadOnly: false} },
		"selection mode":   func(r *Resolved) { r.MCPSelectionMode = "all" },
		"passthrough":      func(r *Resolved) { r.CLIPassthrough = true },
		"exact model":      func(r *Resolved) { r.RequireExactModel = true },
		"fallback":         func(r *Resolved) { r.AutoFallback = true },
		"skip permissions": func(r *Resolved) { r.DangerouslySkipPermissions = true },
		"provider":         func(r *Resolved) { r.ProviderBaseURL = "https://example.test" },
	} {
		changed := resolved()
		mutate(&changed)
		changedDigest, err := changed.Digest()
		require.NoError(t, err, name)
		require.NotEqual(t, digest, changedDigest, name)
	}
}

func TestResolvedRejectsNonCanonicalIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*Resolved){
		"missing profile":   func(r *Resolved) { r.ProfileID = "" },
		"padded profile":    func(r *Resolved) { r.ProfileID = " p " },
		"missing agent":     func(r *Resolved) { r.Agent = "" },
		"missing model":     func(r *Resolved) { r.Model = "" },
		"padded model":      func(r *Resolved) { r.Model = " claude-sonnet-4-6" },
		"padded mode":       func(r *Resolved) { r.Mode = "read-only " },
		"non canonical key": func(r *Resolved) { r.ConfigOptions = map[string]string{" reasoning_effort": "high"} },
		"empty option key":  func(r *Resolved) { r.ConfigOptions = map[string]string{"": "high"} },
	} {
		changed := resolved()
		mutate(&changed)
		_, err := changed.EffectiveProfile()
		require.Error(t, err, name)
	}
}

func TestEffectiveProfileReportsEmptyModeAsDefault(t *testing.T) {
	input := resolved()
	input.Mode = ""
	profile, err := input.EffectiveProfile()
	require.NoError(t, err)
	require.Equal(t, DefaultPermissionMode, profile.PermissionMode)
	require.Equal(t, input.ProfileID, profile.ID)
	require.Equal(t, input.Model, profile.Model)
	digest, err := input.Digest()
	require.NoError(t, err)
	require.Equal(t, digest, profile.ConfigDigest)
}

func TestRequestValidate(t *testing.T) {
	profile, err := resolved().EffectiveProfile()
	require.NoError(t, err)
	valid := Request{
		OrganizationID: "22b11706-52e8-4aaa-98c5-c4eb735ddfd4",
		IssueID:        "d2855262-09ba-4c21-8a45-f2195af568ea",
		RootTaskID:     "8a71a86-99ae-4b9a-acf1-abeb07b4632d",
		WorkspaceID:    "78a71a86-99ae-4b9a-acf1-abeb07b4632d",
		TaskID:         "8a71a86-99ae-4b9a-acf1-abeb07b4632d",
		SessionID:      "session-1",
		Intent:         IntentInvestigation,
		Profile:        profile,
	}
	require.NoError(t, valid.Validate())

	firstAdmission := valid
	firstAdmission.PlanVersion = ""
	require.NoError(t, firstAdmission.Validate())

	for name, mutate := range map[string]func(*Request){
		"no organization":    func(r *Request) { r.OrganizationID = "" },
		"no session":         func(r *Request) { r.SessionID = "" },
		"bad intent":         func(r *Request) { r.Intent = "readonly" },
		"bad plan version":   func(r *Request) { r.PlanVersion = "-plan" },
		"long plan version":  func(r *Request) { r.PlanVersion = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
		"incomplete profile": func(r *Request) { r.Profile.ConfigDigest = "" },
		"readonly mismatch":  func(r *Request) { r.Profile.PermissionMode = "agent" },
	} {
		changed := valid
		mutate(&changed)
		require.Error(t, changed.Validate(), name)
	}

	implementation := valid
	implementation.Intent = IntentImplementation
	require.NoError(t, implementation.Validate())
}

func TestTerminalStateParityWithTaskModels(t *testing.T) {
	for _, state := range []v1.TaskState{
		v1.TaskStateTODO, v1.TaskStateCreated, v1.TaskStateScheduling, v1.TaskStateInProgress,
		v1.TaskStateReview, v1.TaskStateBlocked, v1.TaskStateWaitingForInput,
		v1.TaskStateCompleted, v1.TaskStateFailed, v1.TaskStateCancelled,
	} {
		require.Equal(t, models.IsTerminalTaskState(state), IsTerminalState(state), string(state))
	}
}

func TestDenyTerminalAncestry(t *testing.T) {
	active := []NativeAncestor{
		{ID: "root", State: v1.TaskStateInProgress},
		{ID: "child", State: v1.TaskStateReview},
	}
	require.NoError(t, DenyTerminalAncestry(active))
	require.Error(t, DenyTerminalAncestry(nil))
	require.Error(t, DenyTerminalAncestry([]NativeAncestor{{ID: "", State: v1.TaskStateInProgress}}))
	for _, state := range []v1.TaskState{v1.TaskStateCompleted, v1.TaskStateFailed, v1.TaskStateCancelled} {
		chain := append([]NativeAncestor(nil), active...)
		chain[0].State = state
		require.Error(t, DenyTerminalAncestry(chain), string(state))
	}
	archived := append([]NativeAncestor(nil), active...)
	archived[1].Archived = true
	require.Error(t, DenyTerminalAncestry(archived))
}

func TestDenyIntentChangeAndProfileDrift(t *testing.T) {
	require.NoError(t, DenyIntentChange("", IntentImplementation))
	require.NoError(t, DenyIntentChange(IntentImplementation, IntentImplementation))
	require.Error(t, DenyIntentChange(IntentInvestigation, IntentImplementation))
	require.Error(t, DenyIntentChange(IntentImplementation, IntentInvestigation))

	profile, err := resolved().EffectiveProfile()
	require.NoError(t, err)
	require.NoError(t, DenyProfileDrift(Profile{}, profile))
	require.NoError(t, DenyProfileDrift(profile, profile))
	drifted := profile
	drifted.Model = "other"
	require.Error(t, DenyProfileDrift(profile, drifted))
}
