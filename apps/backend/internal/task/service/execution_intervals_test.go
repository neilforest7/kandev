package service

import (
	"context"
	"testing"
	"time"

	"github.com/kandev/kandev/internal/task/models"
)

// TestListExecutionIntervalsFlow covers the whole accounting contract of the
// execution-interval read in one pass: a measured closed span, an abandoned
// span whose work is unknown, a reservation that was provably never dispatched,
// an open turn that bills while the session is running, the same turn losing
// that credit once the session is terminal, and the authorization boundary.
func TestListExecutionIntervalsFlow(t *testing.T) {
	svc, _, repo := createTestService(t)
	seedScopedWorkspaces(t, repo)
	ctx := context.Background()
	// sess-b is seeded CREATED under ws-b (owner user-b).
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	closedEnd := base.Add(5 * time.Minute)
	closedTurn := &models.Turn{
		ID: "turn-closed", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: base, CompletedAt: &closedEnd, CreatedAt: base, UpdatedAt: base,
	}
	if err := repo.CreateTurn(ctx, closedTurn); err != nil {
		t.Fatalf("create closed turn: %v", err)
	}

	// An open turn that reached dispatch, then the crash sweep abandons it.
	abandonedStart := base.Add(10 * time.Minute)
	abandonedTurn := &models.Turn{
		ID: "turn-abandoned", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: abandonedStart, CreatedAt: abandonedStart, UpdatedAt: abandonedStart,
	}
	if err := repo.CreateTurn(ctx, abandonedTurn); err != nil {
		t.Fatalf("create abandoned turn: %v", err)
	}
	if err := repo.AbandonTurn(ctx, abandonedTurn.ID); err != nil {
		t.Fatalf("abandon turn: %v", err)
	}

	// A reservation that never reached a dispatch attempt, abandoned the same
	// way. Its zero span is measured, not lost.
	neverStart := base.Add(15 * time.Minute)
	neverTurn := &models.Turn{
		ID: "turn-never", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: neverStart, CreatedAt: neverStart, UpdatedAt: neverStart,
		Metadata: map[string]interface{}{models.TurnMetaKeyPromptDispatchPending: true},
	}
	if err := repo.CreateTurn(ctx, neverTurn); err != nil {
		t.Fatalf("create never-dispatched turn: %v", err)
	}
	if err := repo.AbandonTurn(ctx, neverTurn.ID); err != nil {
		t.Fatalf("abandon never-dispatched turn: %v", err)
	}

	// Contradictory row: already declared unconfirmed, yet it also carries the
	// pending flag that would otherwise read as a measured zero. Unconfirmed must
	// win, both when the abandon reclassifies it and when the interval is read.
	contradictoryStart := base.Add(18 * time.Minute)
	contradictoryTurn := &models.Turn{
		ID: "turn-contradictory", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: contradictoryStart, CreatedAt: contradictoryStart, UpdatedAt: contradictoryStart,
		Metadata: map[string]interface{}{
			models.TurnMetaKeyAbandonedUnconfirmed:    true,
			models.TurnMetaKeyPromptDispatchPending:   true,
			models.TurnMetaKeyPromptDispatchAttempted: false,
		},
	}
	if err := repo.CreateTurn(ctx, contradictoryTurn); err != nil {
		t.Fatalf("create contradictory turn: %v", err)
	}
	if err := repo.AbandonTurn(ctx, contradictoryTurn.ID); err != nil {
		t.Fatalf("abandon contradictory turn: %v", err)
	}
	persistedContradictory, err := repo.GetTurn(ctx, contradictoryTurn.ID)
	if err != nil {
		t.Fatalf("read contradictory turn: %v", err)
	}
	if !turnMetadataFlag(persistedContradictory.Metadata, models.TurnMetaKeyAbandonedUnconfirmed) ||
		turnMetadataFlag(persistedContradictory.Metadata, models.TurnMetaKeyAbandonedNotDispatched) {
		t.Fatalf("contradictory metadata %v: want unconfirmed to survive and not-dispatched absent",
			persistedContradictory.Metadata)
	}

	// A provider interruption closes the turn at zero width and records only its
	// own marker: CompletedAt = StartedAt plus interrupted=true, with no proven
	// end. The zero proves nothing about how long it ran, so the read must report
	// unknown instead of a measured zero.
	interruptedStart := base.Add(19 * time.Minute)
	interruptedTurn := &models.Turn{
		ID: "turn-interrupted", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: interruptedStart, CreatedAt: interruptedStart, UpdatedAt: interruptedStart,
	}
	if err := repo.CreateTurn(ctx, interruptedTurn); err != nil {
		t.Fatalf("create interrupted turn: %v", err)
	}
	interruptedRow, err := repo.GetTurn(ctx, interruptedTurn.ID)
	if err != nil {
		t.Fatalf("read interrupted turn before close: %v", err)
	}
	interruptedRow.CompletedAt = &interruptedRow.StartedAt
	interruptedRow.Metadata = map[string]interface{}{models.TurnMetaKeyInterrupted: true}
	if err := repo.UpdateTurn(ctx, interruptedRow); err != nil {
		t.Fatalf("close interrupted turn: %v", err)
	}

	// A message is native dispatch evidence: this pending reservation was never
	// attempted but already owns a message, so it bills as active and, once
	// abandoned, reports unknown rather than a measured zero.
	referencedStart := base.Add(17 * time.Minute)
	referencedTurn := &models.Turn{
		ID: "turn-referenced", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: referencedStart, CreatedAt: referencedStart, UpdatedAt: referencedStart,
		Metadata: map[string]interface{}{models.TurnMetaKeyPromptDispatchPending: true},
	}
	if err := repo.CreateTurn(ctx, referencedTurn); err != nil {
		t.Fatalf("create referenced turn: %v", err)
	}
	// The message hangs off a different session than its turn: native authority
	// matches by turn id alone, so this must still count as dispatch evidence.
	if err := repo.CreateTaskSession(ctx, &models.TaskSession{
		ID: "sess-other", TaskID: "task-b", State: models.TaskSessionStateCreated,
	}); err != nil {
		t.Fatalf("create message-owning session: %v", err)
	}
	if err := repo.CreateMessage(ctx, &models.Message{
		ID: "message-referenced", TaskID: "task-b", TaskSessionID: "sess-other",
		TurnID: referencedTurn.ID, AuthorType: models.MessageAuthorAgent, Content: "durable output",
	}); err != nil {
		t.Fatalf("create referencing message: %v", err)
	}
	if err := repo.UpdateTaskSessionState(ctx, "sess-b", models.TaskSessionStateRunning, ""); err != nil {
		t.Fatalf("mark session running for referenced turn: %v", err)
	}
	beforeAbandon, err := svc.ListExecutionIntervals(ctx, "sess-b")
	if err != nil {
		t.Fatalf("read intervals before abandon: %v", err)
	}
	var beforeReferenced models.ExecutionInterval
	for _, interval := range beforeAbandon.Intervals {
		if interval.TurnID == referencedTurn.ID {
			beforeReferenced = interval
		}
	}
	if beforeReferenced.State != models.ExecutionIntervalStateActive || beforeReferenced.EndedAt != nil {
		t.Fatalf("referenced interval before abandon %+v: want active with no end", beforeReferenced)
	}
	if err := repo.AbandonTurn(ctx, referencedTurn.ID); err != nil {
		t.Fatalf("abandon referenced turn: %v", err)
	}
	persistedReferenced, err := repo.GetTurn(ctx, referencedTurn.ID)
	if err != nil {
		t.Fatalf("read referenced turn after abandon: %v", err)
	}
	if !turnMetadataFlag(persistedReferenced.Metadata, models.TurnMetaKeyAbandonedUnconfirmed) ||
		turnMetadataFlag(persistedReferenced.Metadata, models.TurnMetaKeyAbandonedNotDispatched) {
		t.Fatalf("referenced metadata %v: want unconfirmed and not not-dispatched", persistedReferenced.Metadata)
	}

	openStart := base.Add(20 * time.Minute)
	openTurn := &models.Turn{
		ID: "turn-open", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: openStart, CreatedAt: openStart, UpdatedAt: openStart,
	}
	if err := repo.CreateTurn(ctx, openTurn); err != nil {
		t.Fatalf("create open turn: %v", err)
	}

	// A row whose close is ahead of the read's own clock (a backwards clock
	// move) is contradictory and must fail closed rather than credit time.
	futureStart := base.Add(25 * time.Minute)
	futureEnd := time.Now().UTC().Add(time.Hour)
	futureTurn := &models.Turn{
		ID: "turn-future", TaskSessionID: "sess-b", TaskID: "task-b",
		StartedAt: futureStart, CompletedAt: &futureEnd, CreatedAt: futureStart, UpdatedAt: futureStart,
	}
	if err := repo.CreateTurn(ctx, futureTurn); err != nil {
		t.Fatalf("create future-closed turn: %v", err)
	}
	if err := repo.UpdateTaskSessionState(ctx, "sess-b", models.TaskSessionStateRunning, ""); err != nil {
		t.Fatalf("mark session running: %v", err)
	}

	// The abandon sweep must classify from durable evidence, so the two markers
	// stay distinguishable after the close.
	persistedAbandoned, err := repo.GetTurn(ctx, abandonedTurn.ID)
	if err != nil {
		t.Fatalf("read abandoned turn: %v", err)
	}
	if !turnMetadataFlag(persistedAbandoned.Metadata, models.TurnMetaKeyAbandonedUnconfirmed) {
		t.Fatalf("abandoned turn metadata %v: want unconfirmed marker", persistedAbandoned.Metadata)
	}
	if persistedAbandoned.CompletedAt == nil || !persistedAbandoned.CompletedAt.Equal(persistedAbandoned.StartedAt) {
		t.Fatalf("abandoned turn bounds %v..%v: want a zero-width close",
			persistedAbandoned.StartedAt, persistedAbandoned.CompletedAt)
	}
	persistedNever, err := repo.GetTurn(ctx, neverTurn.ID)
	if err != nil {
		t.Fatalf("read never-dispatched turn: %v", err)
	}
	if !turnMetadataFlag(persistedNever.Metadata, models.TurnMetaKeyAbandonedNotDispatched) {
		t.Fatalf("never-dispatched metadata %v: want not-dispatched marker", persistedNever.Metadata)
	}

	running, err := svc.ListExecutionIntervals(ctx, "sess-b")
	if err != nil {
		t.Fatalf("read intervals on a running session: %v", err)
	}
	if running.SessionID != "sess-b" || running.TaskID != "task-b" {
		t.Fatalf("read bound to %s/%s: want sess-b/task-b", running.SessionID, running.TaskID)
	}
	if len(running.Intervals) != 8 {
		t.Fatalf("read %d intervals: want 8 (%v)", len(running.Intervals), running.Intervals)
	}
	byTurn := map[string]models.ExecutionInterval{}
	for _, interval := range running.Intervals {
		byTurn[interval.TurnID] = interval
	}
	closed := byTurn["turn-closed"]
	if closed.State != models.ExecutionIntervalStateClosed ||
		closed.EndedAt == nil || !closed.EndedAt.Equal(closedEnd) {
		t.Fatalf("closed interval %+v: want closed at %v", closed, closedEnd)
	}
	abandoned := byTurn["turn-abandoned"]
	if abandoned.State != models.ExecutionIntervalStateUnknown || abandoned.EndedAt != nil {
		t.Fatalf("abandoned interval %+v: want unknown with no end", abandoned)
	}
	never := byTurn["turn-never"]
	if never.State != models.ExecutionIntervalStateNotStarted ||
		never.EndedAt == nil || !never.EndedAt.Equal(neverStart) {
		t.Fatalf("never-dispatched interval %+v: want a measured zero span", never)
	}
	open := byTurn["turn-open"]
	if open.State != models.ExecutionIntervalStateActive || open.EndedAt != nil {
		t.Fatalf("open interval %+v: want active with no end", open)
	}
	future := byTurn["turn-future"]
	if future.State != models.ExecutionIntervalStateUnknown || future.EndedAt != nil {
		t.Fatalf("future-closed interval %+v: want unknown with no end", future)
	}
	contradictory := byTurn["turn-contradictory"]
	if contradictory.State != models.ExecutionIntervalStateUnknown || contradictory.EndedAt != nil {
		t.Fatalf("contradictory interval %+v: want unconfirmed to win as unknown with no end", contradictory)
	}
	interrupted := byTurn["turn-interrupted"]
	if interrupted.State != models.ExecutionIntervalStateUnknown || interrupted.EndedAt != nil {
		t.Fatalf("interrupted interval %+v: want unknown with no end", interrupted)
	}
	referenced := byTurn["turn-referenced"]
	if referenced.State != models.ExecutionIntervalStateUnknown || referenced.EndedAt != nil {
		t.Fatalf("referenced interval after abandon %+v: want unknown with no end", referenced)
	}
	if running.Complete {
		t.Fatal("running read reported complete: the abandoned span is unresolved")
	}

	// The same open turn must stop billing the moment the session is terminal,
	// and the read must then present it as unknown rather than credit it up to
	// the terminal timestamp.
	if err := repo.UpdateTaskSessionState(ctx, "sess-b", models.TaskSessionStateFailed, "boom"); err != nil {
		t.Fatalf("mark session failed: %v", err)
	}
	failed, err := svc.ListExecutionIntervals(ctx, "sess-b")
	if err != nil {
		t.Fatalf("read intervals on a failed session: %v", err)
	}
	for _, interval := range failed.Intervals {
		if interval.TurnID != "turn-open" {
			continue
		}
		if interval.State != models.ExecutionIntervalStateUnknown || interval.EndedAt != nil {
			t.Fatalf("orphan interval %+v: want unknown with no end", interval)
		}
	}
	if failed.Complete {
		t.Fatal("failed read reported complete: the orphan span is unresolved")
	}

	// The authorization boundary sits in front of the turn query, and it hides
	// existence: a foreign session reads as not found.
	if _, err := svc.ListExecutionIntervals(ctxAs("user-a"), "sess-b"); err == nil {
		t.Fatal("foreign session read succeeded: want the not-found denial")
	}
	if _, err := svc.ListExecutionIntervals(ctxAs("user-b"), "sess-b"); err != nil {
		t.Fatalf("owner session read: %v", err)
	}
}
