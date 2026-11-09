package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kandev/kandev/internal/agentctl/types/streams"
	"github.com/stretchr/testify/require"
)

// newMCPIdentityTestAdapter returns an adapter wired for the Codex dialect with
// the given current session, so MCP-over-ACP notifications normalize through the
// real parser.
func newMCPIdentityTestAdapter(sessionID string) *Adapter {
	a := newTestAdapter()
	a.agentID = codexAgentID
	a.normalizer = NewNormalizer(codexAgentID)
	a.dialect = newACPDialect(codexAgentID)
	a.sessionID = sessionID
	return a
}

// seedRecognizedMCPNotification drives the actual tool-call notification path so
// the adapter cache holds the payload the production flow would hold, including
// the session the notification was delivered for.
func seedRecognizedMCPNotification(t *testing.T, a *Adapter, sessionID, toolCallID, server, tool string) *streams.NormalizedPayload {
	t.Helper()
	event := a.convertToolCallUpdate(sessionID, &acpsdk.SessionUpdateToolCall{
		Meta:          map[string]any{"is_mcp_tool_call": true},
		Kind:          acpsdk.ToolKind("execute"),
		RawInput:      map[string]any{"server": server, "tool": tool, "arguments": map[string]any{"questions": []any{}}},
		SessionUpdate: "tool_call",
		Status:        acpsdk.ToolCallStatus("in_progress"),
		Title:         "mcp." + server + "." + tool,
		ToolCallId:    acpsdk.ToolCallId(toolCallID),
	})
	require.NotNil(t, event)
	require.NotNil(t, event.NormalizedPayload)
	require.True(t, event.NormalizedPayload.IsMCPTool())
	require.Equal(t, sessionID, event.NormalizedPayload.MCPSourceSessionID())
	return event.NormalizedPayload
}

// capturePermission installs a handler that records the request it receives.
func capturePermission(a *Adapter) **PermissionRequest {
	captured := new(*PermissionRequest)
	a.SetPermissionHandler(func(_ context.Context, req *PermissionRequest) (*PermissionResponse, error) {
		*captured = req
		return &PermissionResponse{OptionID: "allow"}, nil
	})
	return captured
}

func ptrString(value string) *string { return &value }

// TestNamelessMCPApprovalRecoversTrackedIdentity covers the production flow: a
// recognized MCP notification populates the cache, then a nameless approval for
// the same id and session reaches the real handler with the canonical name.
func TestNamelessMCPApprovalRecoversTrackedIdentity(t *testing.T) {
	a := newMCPIdentityTestAdapter("session-1")
	seedRecognizedMCPNotification(t, a, "session-1", "call-1", "kandev", "ask_user_question_kandev")
	captured := capturePermission(a)

	_, err := a.handlePermissionRequest(context.Background(), &PermissionRequest{
		SessionID:  "session-1",
		ToolCallID: "call-1",
		Title:      "MCP tool call approval",
		ActionType: "execute",
	})
	require.NoError(t, err)
	require.NotNil(t, *captured)
	require.NotNil(t, (*captured).ToolName)
	require.Equal(t, "mcp__kandev__ask_user_question_kandev", *(*captured).ToolName)
}

// TestNamelessMCPApprovalLeavesIdentityUnresolved covers every case that must
// keep the existing fail-closed behaviour.
func TestNamelessMCPApprovalLeavesIdentityUnresolved(t *testing.T) {
	assertUnresolved := func(t *testing.T, a *Adapter, req *PermissionRequest) {
		t.Helper()
		captured := capturePermission(a)
		_, err := a.handlePermissionRequest(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, *captured)
		require.Nil(t, (*captured).ToolName)
	}

	t.Run("non-MCP payload with the same title", func(t *testing.T) {
		a := newMCPIdentityTestAdapter("session-1")
		a.activeToolCalls["call-1"] = streams.NewGeneric("kandev/ask_user_question_kandev", nil)
		assertUnresolved(t, a, &PermissionRequest{
			SessionID: "session-1", ToolCallID: "call-1", Title: "MCP tool call approval",
		})
	})

	t.Run("foreign request session", func(t *testing.T) {
		a := newMCPIdentityTestAdapter("session-2")
		seedRecognizedMCPNotification(t, a, "session-1", "call-1", "kandev", "ask_user_question_kandev")
		assertUnresolved(t, a, &PermissionRequest{
			SessionID: "session-1", ToolCallID: "call-1", Title: "MCP tool call approval",
		})
	})

	t.Run("missing request session", func(t *testing.T) {
		a := newMCPIdentityTestAdapter("session-1")
		seedRecognizedMCPNotification(t, a, "session-1", "call-1", "kandev", "ask_user_question_kandev")
		assertUnresolved(t, a, &PermissionRequest{
			ToolCallID: "call-1", Title: "MCP tool call approval",
		})
	})

	t.Run("missing cache entry", func(t *testing.T) {
		a := newMCPIdentityTestAdapter("session-1")
		assertUnresolved(t, a, &PermissionRequest{
			SessionID: "session-1", ToolCallID: "call-1", Title: "MCP tool call approval",
		})
	})

	t.Run("late notification from the replaced session under the same id", func(t *testing.T) {
		// The adapter already bound the new session; a queued notification from
		// the old session still lands in the cache under the same id.
		a := newMCPIdentityTestAdapter("session-2")
		seedRecognizedMCPNotification(t, a, "session-1", "call-1", "kandev", "ask_user_question_kandev")
		assertUnresolved(t, a, &PermissionRequest{
			SessionID: "session-2", ToolCallID: "call-1", Title: "MCP tool call approval",
		})
	})

	t.Run("late notification claimed by its own replaced session", func(t *testing.T) {
		a := newMCPIdentityTestAdapter("session-2")
		seedRecognizedMCPNotification(t, a, "session-1", "call-1", "kandev", "ask_user_question_kandev")
		assertUnresolved(t, a, &PermissionRequest{
			SessionID: "session-1", ToolCallID: "call-1", Title: "MCP tool call approval",
		})
	})

	t.Run("ambiguous slash in tracked name", func(t *testing.T) {
		a := newMCPIdentityTestAdapter("session-1")
		a.activeToolCalls["call-1"] = streams.NewMCPTool("session-1", "server/tool/extra", nil)
		assertUnresolved(t, a, &PermissionRequest{
			SessionID: "session-1", ToolCallID: "call-1", Title: "MCP tool call approval",
		})
	})

	t.Run("present name is preserved", func(t *testing.T) {
		a := newMCPIdentityTestAdapter("session-1")
		seedRecognizedMCPNotification(t, a, "session-1", "call-1", "kandev", "ask_user_question_kandev")
		captured := capturePermission(a)

		_, err := a.handlePermissionRequest(context.Background(), &PermissionRequest{
			SessionID:  "session-1",
			ToolCallID: "call-1",
			ToolName:   ptrString("mcp__kandev__step_complete_kandev"),
			Title:      "MCP tool call approval",
		})
		require.NoError(t, err)
		require.NotNil(t, *captured)
		require.NotNil(t, (*captured).ToolName)
		require.Equal(t, "mcp__kandev__step_complete_kandev", *(*captured).ToolName)
	})
}
