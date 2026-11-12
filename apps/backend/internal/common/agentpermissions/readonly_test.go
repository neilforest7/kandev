package agentpermissions

import "testing"

func TestReadOnlyProviderMode(t *testing.T) {
	for _, tc := range []struct {
		agent, mode string
		want        bool
	}{
		{"claude-acp", "plan", true}, {"claude-acp", "default", false},
		{"claude-acp", "acceptEdits", false}, {"claude-acp", "bypassPermissions", false},
		{"claude-acp", "auto", false}, {"claude-acp", "", false},
		{"codex-acp", "plan", false}, {"pi", "plan", false},
		{"codex-acp", "read-only", true}, {"pi", "read-only", true},
		{"claude", "plan", false}, {"claude-acp", "plan ", false},
	} {
		if got := IsReadOnly(tc.agent, tc.mode); got != tc.want {
			t.Errorf("IsReadOnly(%q, %q) = %v, want %v", tc.agent, tc.mode, got, tc.want)
		}
	}
}
