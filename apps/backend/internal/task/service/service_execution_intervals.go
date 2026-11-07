package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kandev/kandev/internal/task/models"
)

// ListExecutionIntervals reports a session's accounting intervals, one per turn
// row, in start order.
//
// The intervals are the durable accounting source. Session StartedAt and
// CompletedAt bound a whole session incarnation, so a resume that clears
// CompletedAt makes them span downtime that was never agent work. Turn rows
// carry known closed spans instead, and their closure is immutable, so time
// between two intervals — human waits included — is never charged.
//
// Not every turn is measurable. A turn abandoned after an interruption is marked
// unconfirmed and reported as unknown: the part it may have run is dropped rather
// than charged, and the read reports Complete=false. Summing the known spans is
// therefore exact for what the server observed and a lower bound overall.
//
// Complete is false when any interval cannot be accounted for. Callers must
// treat such a result as a lower bound: an unknown interval is not zero work.
func (s *Service) ListExecutionIntervals(
	ctx context.Context,
	sessionID string,
) (*models.SessionExecutionIntervals, error) {
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	// Authorized read: GetTaskSession scopes the caller against the session's
	// task before any turn data is loaded, so a foreign session never reaches
	// the query below.
	session, err := s.GetTaskSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("%w: %s", models.ErrTaskSessionNotFound, sessionID)
	}
	// One snapshot for the rows, the current turn, the state the rows are
	// classified against, and their message evidence.
	turns, activeTurnID, state, referencedTurnIDs, err := s.turns.ReadSessionTurnIntervals(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// Observed clock, read only after the snapshot has committed. A row written
	// while this read was in flight then has bounds at or before now instead of
	// looking like a future timestamp. The same host writes these rows, so a
	// bound genuinely ahead of now means the clock moved backwards; that fails
	// closed as unknown rather than crediting time the server cannot justify.
	now := time.Now().UTC()
	result := &models.SessionExecutionIntervals{
		SessionID: session.ID,
		TaskID:    session.TaskID,
		Complete:  true,
		Intervals: make([]models.ExecutionInterval, 0, len(turns)),
	}
	for _, turn := range turns {
		_, referenced := referencedTurnIDs[turn.ID]
		interval := executionIntervalForTurn(turn, state, activeTurnID, referenced, now)
		if interval == nil {
			continue
		}
		if interval.State == models.ExecutionIntervalStateUnknown {
			result.Complete = false
		}
		result.Intervals = append(result.Intervals, *interval)
	}
	return result, nil
}

// executionIntervalForTurn classifies one turn row. Precedence matters:
//
//   - An existing abandoned-unconfirmed marker outranks every dispatch shortcut,
//     so contradictory flags cannot turn an unaccountable span into a credited
//     zero. A turn a provider interruption closed at zero width is unaccountable
//     in the same way and is checked here too: its zero close proves nothing
//     about how long it ran, and the writer records no end it could prove.
//   - The abandoned-not-dispatched marker outranks the dispatch flags, because
//     the flags are cleared after publication while the marker is permanent.
//   - A referencing message is native dispatch evidence, so it cancels a zero
//     credit and falls through to the completed or current-active branch.
//   - The abandoned-unconfirmed marker outranks the completed timestamp, because
//     that timestamp is a zero-width bookkeeping artifact rather than a
//     measurement of the work the turn may have done.
//   - A span whose close is before its start, or ahead of the read's own
//     clock, is contradictory and is never credited.
//   - Only the session's current open turn can be active, and only while the
//     session state is a known non-terminal one. Anything else that is still
//     open is an orphan the server cannot account for.
func executionIntervalForTurn(
	turn *models.Turn,
	state models.TaskSessionState,
	activeTurnID string,
	referenced bool,
	now time.Time,
) *models.ExecutionInterval {
	if turn == nil {
		return nil
	}
	interval := &models.ExecutionInterval{TurnID: turn.ID, StartedAt: turn.StartedAt}
	// A start the server cannot place in the past is unusable as a bound, and a
	// span that ends before it starts is contradictory. Neither may be credited.
	if turn.StartedAt.IsZero() || turn.StartedAt.After(now) {
		interval.State = models.ExecutionIntervalStateUnknown
		return interval
	}
	if turnMetadataFlag(turn.Metadata, models.TurnMetaKeyAbandonedUnconfirmed) {
		interval.State = models.ExecutionIntervalStateUnknown
		return interval
	}
	if turnMetadataFlag(turn.Metadata, models.TurnMetaKeyInterrupted) {
		interval.State = models.ExecutionIntervalStateUnknown
		return interval
	}
	if turnMetadataFlag(turn.Metadata, models.TurnMetaKeyAbandonedNotDispatched) {
		// A message contradicts the marker, so the zero close is not measurable.
		if referenced {
			interval.State = models.ExecutionIntervalStateUnknown
			return interval
		}
		ended := turn.StartedAt
		interval.EndedAt = &ended
		interval.State = models.ExecutionIntervalStateNotStarted
		return interval
	}
	if turnMetadataFlag(turn.Metadata, models.TurnMetaKeyPromptDispatchPending) &&
		!turnMetadataFlag(turn.Metadata, models.TurnMetaKeyPromptDispatchAttempted) &&
		!referenced {
		ended := turn.StartedAt
		interval.EndedAt = &ended
		interval.State = models.ExecutionIntervalStateNotStarted
		return interval
	}
	if turn.CompletedAt != nil {
		if turn.CompletedAt.Before(turn.StartedAt) || turn.CompletedAt.After(now) {
			interval.State = models.ExecutionIntervalStateUnknown
			return interval
		}
		ended := *turn.CompletedAt
		interval.EndedAt = &ended
		interval.State = models.ExecutionIntervalStateClosed
		return interval
	}
	if turn.ID == activeTurnID && state.AllowsActiveInterval() {
		interval.State = models.ExecutionIntervalStateActive
		return interval
	}
	interval.State = models.ExecutionIntervalStateUnknown
	return interval
}

// turnMetadataFlag reads a durable boolean turn flag. Metadata normally stores
// JSON booleans, but older rows can carry the equivalent string or number form.
func turnMetadataFlag(metadata map[string]interface{}, key string) bool {
	switch typed := metadata[key].(type) {
	case bool:
		return typed
	case string:
		return typed == "true" || typed == "1"
	case float64:
		return typed == 1
	default:
		return false
	}
}
