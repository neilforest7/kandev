package acp

import (
	"context"
	"fmt"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kandev/kandev/internal/agentctl/types/streams"
	"github.com/stretchr/testify/require"
)

func TestClaudeEffortRequiresProviderConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, agent, reported string
		reject, wantError     bool
	}{
		{name: "confirmed", agent: claudeAgentID, reported: "medium"},
		{name: "mismatch", agent: claudeAgentID, reported: "default", wantError: true},
		{name: "missing", agent: claudeAgentID, wantError: true},
		{name: "rejected", agent: claudeAgentID, reject: true, wantError: true},
		{name: "codex unchanged", agent: codexAgentID},
		{name: "pi unchanged", agent: "pi-acp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, provider, _ := newSetModeTestAdapter(t, nil)
			a.agentID = tc.agent
			a.availableConfigOptions = []streams.ConfigOption{{ID: "effort", Type: "select", CurrentValue: "default"}}
			provider.configHandler = func(_ context.Context, request acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
				if request.ValueId == nil || request.ValueId.ConfigId != "effort" || request.ValueId.Value != "medium" {
					return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("unexpected effort request: %+v", request.ValueId)
				}
				if tc.reject {
					return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("unsupported effort")
				}
				var options []acpsdk.SessionConfigOption
				if tc.reported != "" {
					choices := acpsdk.SessionConfigSelectOptionsUngrouped{{Value: "default", Name: "Default"}, {Value: "medium", Name: "Medium"}}
					options = []acpsdk.SessionConfigOption{{Select: &acpsdk.SessionConfigOptionSelect{
						Type: "select", Id: "effort", Name: "Effort", CurrentValue: acpsdk.SessionConfigValueId(tc.reported),
						Options: acpsdk.SessionConfigSelectOptions{Ungrouped: &choices},
					}}}
				}
				return acpsdk.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
			}
			err := a.SetConfigOption(t.Context(), "effort", "medium")
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "medium", a.GetSessionModelState().ConfigOptions[0].CurrentValue)
			}
		})
	}
}

func TestClaudeModeConfigOverridesCachedPlan(t *testing.T) {
	a, provider, _ := newSetModeTestAdapter(t, nil)
	a.agentID = claudeAgentID
	a.noteCurrentMode("session-1", "plan")
	option := groupedModeConfigOption("default")
	option.Select.Id = "mode"
	a.availableConfigOptions = convertACPConfigOptions([]acpsdk.SessionConfigOption{option})
	provider.configHandler = func(_ context.Context, request acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
		if request.ValueId == nil || request.ValueId.ConfigId != "mode" || request.ValueId.Value != "plan" {
			return acpsdk.SetSessionConfigOptionResponse{}, fmt.Errorf("unexpected mode request: %+v", request.ValueId)
		}
		return acpsdk.SetSessionConfigOptionResponse{ConfigOptions: []acpsdk.SessionConfigOption{option}}, nil
	}
	result, err := a.SetMode(t.Context(), "plan")
	require.NoError(t, err)
	require.True(t, result.Confirmed)
	require.Equal(t, "default", result.Effective)
	require.False(t, result.Applied())
}
