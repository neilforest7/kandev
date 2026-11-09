package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kandev/kandev/internal/execadmission"
	mcpprofile "github.com/kandev/kandev/internal/mcp/profile"
	"github.com/kandev/kandev/internal/task/models"
	"github.com/stretchr/testify/require"
)

type staticProfileResolver struct {
	info *AgentProfileInfo
	err  error
}

func (r staticProfileResolver) ResolveProfile(context.Context, string) (*AgentProfileInfo, error) {
	return r.info, r.err
}

func admissionTestInfo(model string) *AgentProfileInfo {
	return &AgentProfileInfo{
		ProfileID:     "profile-1",
		AgentName:     "claude",
		Model:         model,
		Mode:          "agent",
		ConfigOptions: map[string]string{"reasoning_effort": "high"},
	}
}

func TestExecutionAdmissionProfileSurvivesJSONRoundTrip(t *testing.T) {
	profile := execadmission.Profile{ID: "profile-1", Model: "m", PermissionMode: "agent", ConfigDigest: "digest"}
	execution := &AgentExecution{}

	raw, err := json.Marshal(profile)
	require.NoError(t, err)
	var persisted map[string]any
	require.NoError(t, json.Unmarshal(raw, &persisted))
	execution.setMetadataValue(MetadataKeyExecutionAdmissionProfile, persisted)

	restored, ok := executionAdmissionProfile(execution)
	require.True(t, ok)
	require.Equal(t, profile, restored)

	execution.setMetadataValue(MetadataKeyExecutionAdmissionProfile, map[string]any{"id": "profile-1"})
	_, ok = executionAdmissionProfile(execution)
	require.False(t, ok, "an incomplete persisted profile never admits")
}

func TestAdmittedProfileChangeIsDeniedBeforeMutation(t *testing.T) {
	manager := &Manager{}
	execution := &AgentExecution{ID: "execution-1", SessionID: "session-1", TaskID: "task-1"}
	// Without the gate, live changes keep their pre-existing behavior.
	require.NoError(t, manager.denyAdmittedProfileChange(execution))

	manager.SetExecutionAdmission(func(context.Context, execadmission.Candidate) error { return nil })
	require.NoError(t, manager.denyAdmittedProfileChange(execution), "an un-admitted execution is not pinned")

	profile, err := executionProfile("claude", admissionTestInfo("model"), "", nil)
	require.NoError(t, err)
	execution.setMetadataValue(MetadataKeyExecutionAdmissionProfile, profile)
	require.Error(t, manager.denyAdmittedProfileChange(execution))
}

func TestAdmitRecordedExecutionRequiresFreshConfigurationMatch(t *testing.T) {
	info := admissionTestInfo("model")
	recorded, err := executionProfile(info.AgentName, info, "", nil)
	require.NoError(t, err)

	var gated []execadmission.Profile
	manager := &Manager{profileResolver: staticProfileResolver{info: info}}
	manager.SetExecutionAdmission(func(_ context.Context, candidate execadmission.Candidate) error {
		gated = append(gated, candidate.Profile)
		return nil
	})
	execution := &AgentExecution{SessionID: "session-1", TaskID: "task-1"}
	execution.setMetadataValue(MetadataKeyExecutionAdmissionProfile, recorded)

	require.NoError(t, manager.admitRecordedExecution(context.Background(), execution))
	require.Equal(t, []execadmission.Profile{recorded}, gated)

	changed := admissionTestInfo("another-model")
	manager.profileResolver = staticProfileResolver{info: changed}
	err = manager.admitRecordedExecution(context.Background(), execution)
	require.Error(t, err, "a configuration change after admission denies the next prompt")
	require.Len(t, gated, 1, "a drifted profile is refused before the bridge")

	manager.profileResolver = staticProfileResolver{err: errors.New("resolve failed")}
	require.Error(t, manager.admitRecordedExecution(context.Background(), execution))
}

func TestAdmitRecoveredExecutionRebuildsTheBoundProfile(t *testing.T) {
	info := admissionTestInfo("model")
	manager := &Manager{profileResolver: staticProfileResolver{info: info}}
	var gated []execadmission.Profile
	manager.SetExecutionAdmission(func(_ context.Context, candidate execadmission.Candidate) error {
		gated = append(gated, candidate.Profile)
		return nil
	})
	execution := &AgentExecution{SessionID: "session-1", TaskID: "task-1", ExecutionProfileID: info.ProfileID}

	require.NoError(t, manager.admitRecoveredExecution(context.Background(), execution))
	require.Len(t, gated, 1)
	recorded, ok := executionAdmissionProfile(execution)
	require.True(t, ok)
	require.Equal(t, gated[0], recorded)

	// The next admission reuses the recorded profile instead of resolving again.
	require.NoError(t, manager.admitRecoveredExecution(context.Background(), execution))
	require.Len(t, gated, 2)
	require.Equal(t, gated[0], gated[1])
}

func TestAdmitResolvedLaunchWithoutGateIsUnchanged(t *testing.T) {
	manager := &Manager{}
	profile, err := manager.admitResolvedLaunch(context.Background(), &LaunchRequest{SessionID: "s", TaskID: "t"}, "claude", admissionTestInfo("model"))
	require.NoError(t, err)
	require.Empty(t, profile.ID)

	denied := errors.New("approval denied")
	manager.SetExecutionAdmission(func(context.Context, execadmission.Candidate) error { return denied })
	_, err = manager.admitResolvedLaunch(context.Background(), &LaunchRequest{SessionID: "s", TaskID: "t"}, "claude", admissionTestInfo("model"))
	require.ErrorIs(t, err, denied)

	manager.SetExecutionAdmission(func(_ context.Context, candidate execadmission.Candidate) error {
		require.Equal(t, "s", candidate.SessionID)
		return nil
	})
	admitted, err := manager.admitResolvedLaunch(context.Background(), &LaunchRequest{SessionID: "s", TaskID: "t"}, "claude", admissionTestInfo("model"))
	require.NoError(t, err)
	require.Equal(t, "profile-1", admitted.ID)
}

func TestExecutionRosterMatchesAdmissionFingerprint(t *testing.T) {
	info := admissionTestInfo("model")
	manager := &Manager{profileResolver: staticProfileResolver{info: info}}
	manager.SetExecutionMCPPolicyResolver(func(context.Context, string, string) (*mcpprofile.Context, error) {
		return &mcpprofile.Context{Surface: mcpprofile.SurfaceKanbanTask, Providers: []string{"kandev"}}, nil
	})
	manager.SetExecutionRuntimeResolver(func(context.Context, string, string) (string, error) {
		return string(models.ExecutorType("local").Runtime()), nil
	})
	roster, err := manager.ExecutionRoster(context.Background(), "task-1", []string{info.ProfileID, info.ProfileID})
	require.NoError(t, err)
	require.Len(t, roster, 1)

	recorded, err := executionProfile(info.AgentName, info, string(models.ExecutorType("local").Runtime()), &mcpprofile.Context{Surface: mcpprofile.SurfaceKanbanTask, Providers: []string{"kandev"}})
	require.NoError(t, err)
	require.Equal(t, recorded, roster[0])
	require.Equal(t, info.ProfileID, roster[0].ID)
	require.Equal(t, info.Model, roster[0].Model)
	require.Equal(t, "agent", roster[0].PermissionMode)
	require.NotEmpty(t, roster[0].ConfigDigest)

	_, err = manager.ExecutionRoster(context.Background(), "task-1", nil)
	require.Error(t, err)

	withoutRuntime := &Manager{profileResolver: staticProfileResolver{info: info}}
	withoutRuntime.SetExecutionMCPPolicyResolver(manager.executionMCPPolicy)
	_, err = withoutRuntime.ExecutionRoster(context.Background(), "task-1", []string{info.ProfileID})
	require.Error(t, err, "the roster refuses to guess an empty runtime")
	_, err = (&Manager{}).ExecutionRoster(context.Background(), "task-1", []string{info.ProfileID})
	require.Error(t, err)
}

// An ordinary non-Linear launch must not need a concrete profile: the gate asks
// the native scope first, so a base profile with no model still launches.
func TestNonLinearLaunchDoesNotRequireAConcreteProfile(t *testing.T) {
	manager := &Manager{}
	gated := false
	manager.SetExecutionAdmission(func(context.Context, execadmission.Candidate) error {
		gated = true
		return nil
	})
	manager.SetExecutionScope(func(context.Context, string, string) (bool, error) { return false, nil })

	base := &AgentProfileInfo{ProfileID: "base", AgentName: "claude"}
	profile, err := manager.admitResolvedLaunch(context.Background(), &LaunchRequest{SessionID: "session-1", TaskID: "task-1"}, "claude", base)
	if err != nil {
		t.Fatalf("ordinary launch was gated: %v", err)
	}
	if profile.ID != "" {
		t.Fatalf("out-of-scope launch recorded a profile: %+v", profile)
	}
	if gated {
		t.Fatal("out-of-scope launch reached the bridge gate")
	}

	// The same launch under a watch root is gated and fails on the empty model.
	manager.SetExecutionScope(func(context.Context, string, string) (bool, error) { return true, nil })
	if _, err := manager.admitResolvedLaunch(context.Background(), &LaunchRequest{SessionID: "session-1", TaskID: "task-1"}, "claude", base); err == nil {
		t.Fatal("a watch launch without a concrete model must deny")
	}

	// A watch task launched without a session is still in scope: the gate must
	// see it and deny on the missing native session instead of skipping.
	var reached []execadmission.Candidate
	denyMissingSession := errors.New("linear watch execution requires an approved plan")
	manager.SetExecutionAdmission(func(_ context.Context, candidate execadmission.Candidate) error {
		reached = append(reached, candidate)
		return denyMissingSession
	})
	manager.SetExecutionScope(func(context.Context, string, string) (bool, error) { return true, nil })
	if _, err := manager.admitResolvedLaunch(context.Background(), &LaunchRequest{TaskID: "watch-root"}, "claude", admissionTestInfo("model")); !errors.Is(err, denyMissingSession) {
		t.Fatalf("watch launch without a session must deny, got %v", err)
	}
	require.Len(t, reached, 1)
	require.Empty(t, reached[0].SessionID)
	require.Equal(t, "watch-root", reached[0].TaskID)

	scopeErr := errors.New("scope unavailable")
	manager.SetExecutionScope(func(context.Context, string, string) (bool, error) { return false, scopeErr })
	if _, err := manager.admitResolvedLaunch(context.Background(), &LaunchRequest{SessionID: "session-1", TaskID: "task-1"}, "claude", base); !errors.Is(err, scopeErr) {
		t.Fatalf("scope failure must deny, got %v", err)
	}
}
