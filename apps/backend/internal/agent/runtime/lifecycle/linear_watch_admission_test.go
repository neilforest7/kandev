package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	agentctl "github.com/kandev/kandev/internal/agent/runtime/agentctl"
	"github.com/kandev/kandev/internal/agentctl/types/streams"
)

type countingAdmissionRunner struct {
	executionPassthroughRunner
	writes int
}

func (r *countingAdmissionRunner) WriteStdin(string, string) error {
	r.writes++
	return nil
}

func TestExecutorAdmissionBlocksSteerAndPassthrough(t *testing.T) {
	h := newSteerHarness(t)
	foreground := h.startForegroundTurn(t, 1)
	denied := errors.New("business approval required")
	h.mgr.SetExecutorAdmission(func(ctx context.Context, _, _, _ string) (context.Context, error) { return ctx, denied })
	if _, err := h.mgr.sessionManager.SendPromptSteerWithDispatchCallback(h.ctx, h.exec, "steer", false, nil, true, nil); !errors.Is(err, denied) {
		t.Fatalf("steer: %v", err)
	}
	select {
	case got := <-h.probe.ch:
		t.Fatalf("unexpected steer RPC: %+v", got)
	default:
	}
	runner := &countingAdmissionRunner{}
	h.mgr.passthroughRunnerOverride = runner
	h.exec.PassthroughProcessID = "pty"
	if err := h.mgr.WritePassthroughStdin(h.ctx, h.exec.SessionID, "write"); !errors.Is(err, denied) {
		t.Fatalf("stdin: %v", err)
	}
	if runner.writes != 0 {
		t.Fatalf("stdin writes = %d", runner.writes)
	}
	if err := h.mgr.AdmitTerminalInput(h.ctx, h.exec.SessionID); !errors.Is(err, denied) {
		t.Fatalf("direct terminal: %v", err)
	}
	h.mgr.handleAgentEvent(h.exec, agentctl.AgentEvent{Type: streams.EventTypeComplete, PromptGeneration: 1, Data: map[string]any{"stop_reason": "end_turn"}})
	select {
	case <-foreground:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground turn did not finish")
	}
}
