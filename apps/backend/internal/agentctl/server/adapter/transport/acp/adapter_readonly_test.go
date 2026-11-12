package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"
)

func requireClaudeReadOnlyMeta(t *testing.T, meta map[string]any) {
	t.Helper()
	claude, ok := meta["claudeCode"].(map[string]any)
	require.True(t, ok)
	options, ok := claude["options"].(map[string]any)
	require.True(t, ok)
	// Requests cross JSON-RPC, so inspect the serialized array shapes.
	require.Equal(t, []any{"Read", "Grep", "Glob"}, options["tools"])
	require.Equal(t, []any{}, options["settingSources"])
	require.Equal(t, true, options["strictMcpConfig"])
	require.Equal(t, false, options["allowDangerouslySkipPermissions"])
	_, preapprovals := options["allowedTools"]
	require.False(t, preapprovals)
}

func TestClaudeReadOnlySessionPolicy(t *testing.T) {
	for _, suggestions := range []bool{false, true} {
		a, fake := newSessionRequestCaptureAdapter(t, acpsdk.McpCapabilities{})
		a.cfg.AgentID, a.cfg.ReadOnly = "claude-acp", true
		a.promptSuggestionSupport, a.cfg.PromptSuggestions = suggestions, suggestions
		_, err := a.NewSession(t.Context(), nil)
		require.NoError(t, err)
		require.NoError(t, a.LoadSession(t.Context(), "saved", nil))
		a.capabilities.SessionCapabilities.Resume = &acpsdk.SessionResumeCapabilities{}
		require.NoError(t, a.ResumeSession(t.Context(), "saved", nil))
		for _, meta := range []map[string]any{fake.newRequest.Meta, fake.loadRequest.Meta, fake.resumeRequest.Meta} {
			requireClaudeReadOnlyMeta(t, meta)
			if suggestions {
				requireClaudeSuggestionMeta(t, meta, true)
			}
		}
	}
	// Load prefers resume when advertised; its method-not-found fallback must
	// retain exactly the same restriction when it falls back to legacy load.
	a, fake := newSessionResumeAdapter(t, true, true)
	a.cfg.AgentID, a.cfg.ReadOnly = "claude-acp", true
	fake.resumeError = acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionResume)
	require.NoError(t, a.LoadSession(t.Context(), "saved", nil))
	requireClaudeReadOnlyMeta(t, fake.resumeRequest.Meta)
	requireClaudeReadOnlyMeta(t, fake.loadRequest.Meta)
}

func TestClaudeReadOnlyPolicyDoesNotLeakToOtherSessions(t *testing.T) {
	for _, tc := range []struct {
		agent    string
		readOnly bool
	}{
		{"claude-acp", false}, {"codex-acp", true}, {"pi", true},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			a, fake := newSessionRequestCaptureAdapter(t, acpsdk.McpCapabilities{})
			a.cfg.AgentID, a.cfg.ReadOnly = tc.agent, tc.readOnly
			_, err := a.NewSession(t.Context(), nil)
			require.NoError(t, err)
			requireNoSuggestionMeta(t, fake.newRequest.Meta)
		})
	}
}
