package linear

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	internaldb "github.com/kandev/kandev/internal/db"
)

func TestWatchRecoveryUsesStableIssueOutsideFilter(t *testing.T) {
	ctx := context.Background()
	f := newSvcFixture(t)
	databasePath := filepath.Join(t.TempDir(), "watch.db")
	openStore := func() (*Store, *sqlx.DB) {
		db, err := sqlx.Open("sqlite3", databasePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		db.SetMaxOpenConns(1)
		store, err := NewStore(db, db)
		if err != nil {
			t.Fatal(err)
		}
		return store, db
	}
	store, db := openStore()
	f.store, f.svc.store = store, store
	if err := f.store.UpsertConfigForWorkspace(ctx, "ws-1", &LinearConfig{AuthMethod: AuthMethodAPIKey}); err != nil {
		t.Fatal(err)
	}
	if err := f.secrets.Set(ctx, SecretKeyForWorkspace("ws-1"), "Linear", "test"); err != nil {
		t.Fatal(err)
	}
	watch := newTestIssueWatch("ws-1")
	if err := f.store.CreateIssueWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	issueID := uuid.NewString()
	old := &LinearIssue{ID: issueID, OrganizationID: "00000000-0000-4000-8000-000000000001", Identifier: "ENG-1", URL: "https://linear.app/x/issue/ENG-1"}
	if ok, err := f.store.ReserveIssueWatchTask(ctx, watch.ID, old); err != nil || !ok {
		t.Fatalf("reserve: %v, %v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, _ = openStore()
	f.store, f.svc.store = store, store
	f.client.testAuthFn = func() (*TestConnectionResult, error) {
		return &TestConnectionResult{OK: true, OrganizationID: "00000000-0000-4000-8000-000000000099"}, nil
	}
	if _, err := f.svc.CheckIssueWatch(ctx, watch); err == nil {
		t.Fatal("a different credential organization must block recovery")
	}
	f.client.testAuthFn = nil
	f.client.getIssueFn = func(id string) (*LinearIssue, error) {
		if id != issueID {
			t.Fatalf("recovery queried %q, want issue UUID", id)
		}
		return &LinearIssue{ID: issueID, Identifier: "NEW-9", URL: "https://linear.app/x/issue/NEW-9"}, nil
	}
	f.client.withSearchResults(nil)
	issues, err := f.svc.CheckIssueWatch(ctx, watch)
	if err != nil || len(issues) != 1 {
		t.Fatalf("recover outside filter: %+v, %v", issues, err)
	}
	recovered := issues[0]
	if !recovered.RecoveryPending || recovered.Identifier != "NEW-9" || recovered.ReservationIdentifier != old.Identifier || recovered.ReservationURL != old.URL {
		t.Fatalf("recovered issue = %+v", recovered)
	}
	if ok, err := f.store.ReserveIssueWatchTask(ctx, watch.ID, recovered); err != nil || !ok {
		t.Fatalf("retry reserve: %v, %v", ok, err)
	}
	if err := assignTestIssueWatchTaskID(ctx, f.store, watch.ID, recovered.ReservationIdentifier, "task-1"); err != nil {
		t.Fatal(err)
	}
	link, err := f.store.GetTaskIssueLink(ctx, "task-1")
	if err != nil || link.IssueID != issueID || link.IssueIdentifier != old.Identifier || link.IssueURL != old.URL {
		t.Fatalf("source link: %+v, %v", link, err)
	}
	f.client.withSearchResults([]LinearIssue{{ID: issueID, Identifier: "NEW-9", URL: recovered.URL}})
	issues, err = f.svc.CheckIssueWatch(ctx, watch)
	if err != nil || len(issues) != 0 {
		t.Fatalf("attached issue rediscovered after rename: %+v, %v", issues, err)
	}
}

func TestWatchRecoveryBlocksReservationWithoutStableIdentity(t *testing.T) {
	ctx := context.Background()
	f := newSvcFixture(t)
	if err := f.store.UpsertConfigForWorkspace(ctx, "ws-1", &LinearConfig{AuthMethod: AuthMethodAPIKey}); err != nil {
		t.Fatal(err)
	}
	if err := f.secrets.Set(ctx, SecretKeyForWorkspace("ws-1"), "Linear", "test"); err != nil {
		t.Fatal(err)
	}
	watch := newTestIssueWatch("ws-1")
	if err := f.store.CreateIssueWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(ctx, `INSERT INTO linear_issue_watch_tasks (id, issue_watch_id, issue_identifier, issue_url, task_id, created_at) VALUES (?, ?, ?, ?, '', CURRENT_TIMESTAMP)`, uuid.NewString(), watch.ID, "ENG-1", "https://linear.app/x/issue/ENG-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CheckIssueWatch(ctx, watch); err == nil || !strings.Contains(err.Error(), "no stable issue identity") {
		t.Fatalf("legacy reservation must block recovery: %v", err)
	}
	rows, err := f.store.ListIssueWatchReservations(ctx, watch.ID)
	if err != nil || len(rows) != 1 || rows[0].TaskID != "" {
		t.Fatalf("legacy reservation was changed: %+v, %v", rows, err)
	}
}

func TestWatchRecoveryDoesNotRecreateRemovedReservation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	watch := newTestIssueWatch("ws-1")
	if err := store.CreateIssueWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	issue := &LinearIssue{OrganizationID: "00000000-0000-4000-8000-000000000001", ID: uuid.NewString(), Identifier: "ENG-1", URL: "https://linear.app/x/issue/ENG-1"}
	if ok, err := store.ReserveIssueWatchTask(ctx, watch.ID, issue); err != nil || !ok {
		t.Fatalf("reserve: %v, %v", ok, err)
	}
	issue.RecoveryPending = true
	issue.ReservationIdentifier, issue.ReservationURL = issue.Identifier, issue.URL
	if err := store.ResetIssueWatchState(ctx, watch.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ReserveIssueWatchTask(ctx, watch.ID, issue); err == nil || ok {
		t.Fatalf("removed reservation was recreated: %v, %v", ok, err)
	}
	rows, err := store.ListIssueWatchReservations(ctx, watch.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("reservations after reset: %+v, %v", rows, err)
	}
}

func TestWatchRecoveryRejectsStaleAttachmentAfterReset(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	watch := newTestIssueWatch("ws-1")
	if err := store.CreateIssueWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	old := &LinearIssue{OrganizationID: "00000000-0000-4000-8000-000000000001", ID: uuid.NewString(), Identifier: "ENG-1", URL: "https://linear.app/x/issue/ENG-1"}
	if ok, err := store.ReserveIssueWatchTask(ctx, watch.ID, old); err != nil || !ok {
		t.Fatalf("first reserve: %v, %v", ok, err)
	}
	oldGeneration := old.ReservationID
	second := *old
	if ok, err := store.ReserveIssueWatchTask(ctx, watch.ID, &second); err != nil || !ok || second.ReservationID != oldGeneration {
		t.Fatalf("second reserve: %v, %v, %q", ok, err, second.ReservationID)
	}
	if err := store.AssignIssueWatchTaskID(ctx, watch.ID, old.Identifier, oldGeneration, "task-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetIssueWatchState(ctx, watch.ID); err != nil {
		t.Fatal(err)
	}
	fresh := *old
	fresh.ReservationID = ""
	if ok, err := store.ReserveIssueWatchTask(ctx, watch.ID, &fresh); err != nil || !ok || fresh.ReservationID == oldGeneration {
		t.Fatalf("replacement reserve: %v, %v, %q", ok, err, fresh.ReservationID)
	}
	second.RecoveryPending = true
	second.ReservationIdentifier, second.ReservationURL = second.Identifier, second.URL
	if ok, err := store.ReserveIssueWatchTask(ctx, watch.ID, &second); err == nil || ok {
		t.Fatalf("stale recovery adopted replacement: %v, %v", ok, err)
	}
	if err := store.AssignIssueWatchTaskID(ctx, watch.ID, old.Identifier, oldGeneration, "task-a"); err == nil {
		t.Fatal("stale attachment changed replacement reservation")
	}
	for i := 0; i < 2; i++ {
		if err := store.AssignIssueWatchTaskID(ctx, watch.ID, fresh.Identifier, fresh.ReservationID, "task-b"); err != nil {
			t.Fatalf("replacement attach %d: %v", i, err)
		}
	}
	if err := store.AssignIssueWatchTaskID(ctx, watch.ID, fresh.Identifier, fresh.ReservationID, "task-c"); err == nil {
		t.Fatal("conflicting attachment succeeded")
	}
	if _, err := store.GetTaskIssueLink(ctx, "task-b"); err != nil {
		t.Fatalf("replacement source association: %v", err)
	}
}

func TestWatchConcurrentReservationsShareGeneration(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "concurrent-watch.db")
	stores := make([]*Store, 2)
	for i := range stores {
		raw, err := internaldb.OpenSQLite(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = raw.Close() })
		db := sqlx.NewDb(raw, "sqlite3")
		stores[i], err = NewStore(db, db)
		if err != nil {
			t.Fatal(err)
		}
	}
	watch := newTestIssueWatch("ws-1")
	if err := stores[0].CreateIssueWatch(ctx, watch); err != nil {
		t.Fatal(err)
	}
	issue := LinearIssue{OrganizationID: "00000000-0000-4000-8000-000000000001", ID: uuid.NewString(), Identifier: "ENG-1", URL: "https://linear.app/x/issue/ENG-1"}
	type result struct {
		ok  bool
		id  string
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for _, store := range stores {
		workers.Add(1)
		go func() {
			defer workers.Done()
			candidate := issue
			<-start
			ok, err := store.ReserveIssueWatchTask(ctx, watch.ID, &candidate)
			results <- result{ok: ok, id: candidate.ReservationID, err: err}
		}()
	}
	close(start)
	workers.Wait()
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || !first.ok || !second.ok || first.id == "" || first.id != second.id {
		t.Fatalf("concurrent reservations = %+v, %+v", first, second)
	}
}
