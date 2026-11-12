package lifecycle

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kandev/kandev/internal/agent/agents"
	agentctl "github.com/kandev/kandev/internal/agent/runtime/agentctl"
	"github.com/kandev/kandev/internal/agentctl/types/streams"
	"github.com/kandev/kandev/pkg/agent"
	ws "github.com/kandev/kandev/pkg/websocket"
	"github.com/stretchr/testify/require"
)

func TestClaudeEffortBootstrapBeforePrompt(t *testing.T) {
	for _, tc := range []struct {
		name, agent, existing         string
		reject, runtime, modeMismatch bool
	}{
		{name: "new", agent: "claude-acp"},
		{name: "restore", agent: "claude-acp", existing: "saved-session"},
		{name: "profile rejected", agent: "claude-acp", reject: true},
		{name: "runtime rejected", agent: "claude-acp", reject: true, runtime: true},
		{name: "restore mode mismatch", agent: "claude-acp", existing: "saved-session", modeMismatch: true},
		{name: "codex unchanged", agent: "codex-acp", reject: true},
		{name: "pi unchanged", agent: "pi-acp", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockAgentServer(t)
			t.Cleanup(mock.Close)
			mock.handler = func(msg ws.Message) *ws.Message {
				switch msg.Action {
				case "agent.session.set_mode":
					if tc.modeMismatch {
						response, _ := ws.NewResponse(msg.ID, msg.Action, agentctl.ModeResult{
							Requested: "plan", Effective: "default", Confirmed: true,
						})
						return response
					}
				case "agent.session.new", "agent.session.load":
					response, _ := ws.NewResponse(msg.ID, msg.Action, map[string]any{
						"success": true, "session_id": "test-session",
						"model_state": streams.SessionModelState{
							ConfigOptions: []streams.ConfigOption{{ID: "effort", Type: "select", CurrentValue: "default",
								Options: []streams.ConfigOptionValue{{Value: "medium"}, {Value: "default"}}}},
							ConfigOptionsSettled: true,
						},
					})
					return response
				case "agent.session.set_config_option":
					var request struct {
						ConfigID string `json:"config_id"`
						Value    string `json:"value"`
					}
					if err := msg.ParsePayload(&request); err != nil || request.ConfigID != "effort" || request.Value != "medium" || tc.reject {
						response, _ := ws.NewError(msg.ID, msg.Action, ws.ErrorCodeInternalError, "effort not confirmed", nil)
						return response
					}
				}
				return mock.defaultHandler(msg)
			}
			stopCh := newTestStopCh(t)
			log := newSessionTestLogger()
			sm := NewSessionManager(log, stopCh)
			sm.SetDependencies(NewEventPublisher(&MockEventBusWithTracking{}, log), nil, nil, nil)
			client := createTestClient(t, mock.server.URL)
			disconnected := connectAgentStream(t, mock, client)
			t.Cleanup(func() {
				closeStopChOnce(stopCh)
				client.Close()
				select {
				case <-disconnected:
				case <-time.After(5 * time.Second):
					t.Error("agent stream did not drain")
				}
			})
			execution := &AgentExecution{
				ID: "effort-exec", TaskID: "effort-task", SessionID: "effort-session", AgentID: tc.agent,
				ACPSessionID: tc.existing, WorkspacePath: t.TempDir(), agentctl: client,
				promptDoneCh: make(chan PromptCompletionSignal, 1),
			}
			if tc.modeMismatch {
				execution.SetModeState(&CachedModeState{CurrentModeID: "plan"})
			}
			promptDispatched := make(chan struct{}, 1)
			execution.setInitialPromptDispatchCallbacks(nil, func() {
				promptDispatched <- struct{}{}
				execution.promptDoneCh <- PromptCompletionSignal{StopReason: "test-complete"}
			}, nil)
			agentConfig := &testAgent{id: tc.agent, enabled: true, runtimeConfig: &agents.RuntimeConfig{
				Cmd: agents.NewCommand(tc.agent), Protocol: agent.ProtocolACP,
				SessionConfig: agents.SessionConfig{NativeSessionResume: tc.existing != ""},
			}}
			profileOptions := map[string]string{"effort": "medium"}
			var runtimeOptions map[string]string
			if tc.runtime {
				runtimeOptions, profileOptions = profileOptions, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err := sm.InitializeAndPromptWithLayers(ctx, execution, agentConfig, "review", nil, nil,
				func(string) error { return nil }, "", "plan", profileOptions, "", "", runtimeOptions, StartModelPolicy{})
			if tc.agent == "claude-acp" && (tc.reject || tc.modeMismatch) {
				require.Error(t, err)
				require.False(t, execution.isSessionInitialized())
				require.NotContains(t, mock.getActionLog(), "agent.prompt")
				return
			}
			require.NoError(t, err)
			select {
			case <-promptDispatched:
			case <-ctx.Done():
				t.Fatal("initial prompt was not dispatched")
			}
			actions := mock.getActionLog()
			configIndex := slices.Index(actions, "agent.session.set_config_option")
			sessionAction := "agent.session.new"
			if tc.existing != "" {
				sessionAction = "agent.session.load"
			}
			require.GreaterOrEqual(t, slices.Index(actions, sessionAction), 0)
			require.Greater(t, configIndex, slices.Index(actions, sessionAction))
			require.Greater(t, slices.Index(actions, "agent.prompt"), configIndex)
		})
	}
}
