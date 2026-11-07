package linear

import (
	"context"
	"errors"
	"testing"
)

func TestWatchReservationRestartAndAssociation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	watch := newTestIssueWatch("ws-1")
	if err := store.CreateIssueWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if ok, err := reserveTestIssueWatchTask(ctx, store, watch.ID, "ENG-1", "https://linear.app/x/issue/ENG-1"); err != nil || !ok {
		t.Fatalf("reserve: %v, %v", ok, err)
	}

	// Reconstruct the store and verify that an incomplete reservation is retried.
	restarted, err := NewStore(store.db, store.ro)
	if err != nil {
		t.Fatal(err)
	}
	seen, err := restarted.ListIssueWatchReservations(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].IssueIdentifier != "ENG-1" {
		t.Fatal("reservation disappeared on restart")
	}
	if seen[0].TaskID != "" {
		t.Fatalf("pending reservation unexpectedly attached: %+v", seen[0])
	}
	if ok, err := reserveTestIssueWatchTask(ctx, restarted, watch.ID, "ENG-1", "https://linear.app/x/issue/ENG-1"); err != nil || !ok {
		t.Fatalf("retry reserve: %v, %v", ok, err)
	}
	if _, err := restarted.GetTaskIssueLink(ctx, "task-1"); !errors.Is(err, ErrSourceLinkNotFound) {
		t.Fatalf("unattached task link: %v", err)
	}
	if err := assignTestIssueWatchTaskID(ctx, restarted, watch.ID, "ENG-1", "task-1"); err != nil {
		t.Fatal(err)
	}
	if err := assignTestIssueWatchTaskID(ctx, restarted, watch.ID, "ENG-1", "task-1"); err != nil {
		t.Fatalf("repeat attach: %v", err)
	}
	if err := assignTestIssueWatchTaskID(ctx, restarted, watch.ID, "ENG-1", "task-2"); err == nil {
		t.Fatal("conflicting attach overwrote existing association")
	}
	if _, err := restarted.GetTaskIssueLink(ctx, "task-1"); err != nil {
		t.Fatalf("original host association lost: %v", err)
	}
	attached, err := restarted.ListIssueWatchReservations(ctx, watch.ID)
	if err != nil || len(attached) != 1 || attached[0].TaskID != "task-1" {
		t.Fatalf("attached reservation must be seen: %v, %v", attached, err)
	}
}
