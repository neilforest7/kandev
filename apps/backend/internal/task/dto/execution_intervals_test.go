package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kandev/kandev/internal/task/models"
)

// The bridge relay decodes strictly, so endedAt must be present and null for
// every interval without a measured end. An omitted key is rejected there, and
// `omitempty` would silently reintroduce the omission.
func TestFromSessionExecutionIntervalsEmitsNullEndedAt(t *testing.T) {
	now := time.Now().UTC()
	encoded, err := json.Marshal(FromSessionExecutionIntervals(&models.SessionExecutionIntervals{
		SessionID: "session-1",
		TaskID:    "task-1",
		Complete:  false,
		Intervals: []models.ExecutionInterval{
			{TurnID: "turn-active", StartedAt: now, State: models.ExecutionIntervalStateActive},
			{TurnID: "turn-unknown", StartedAt: now, State: models.ExecutionIntervalStateUnknown},
		},
	}))
	if err != nil {
		t.Fatalf("marshal intervals: %v", err)
	}

	var decoded struct {
		Intervals []map[string]json.RawMessage `json:"intervals"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode intervals: %v", err)
	}
	if len(decoded.Intervals) != 2 {
		t.Fatalf("decoded %d intervals: want 2", len(decoded.Intervals))
	}
	for _, interval := range decoded.Intervals {
		raw, present := interval["endedAt"]
		if !present {
			t.Fatalf("interval %v omits endedAt: the relay requires an explicit null", interval)
		}
		if string(raw) != "null" {
			t.Fatalf("interval endedAt = %s: want null", raw)
		}
	}
}
