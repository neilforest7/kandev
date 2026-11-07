package linear

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kandev/kandev/internal/db/dialect"
	executoridentity "github.com/kandev/kandev/internal/executors/identity"
	"time"

	"github.com/google/uuid"
)

func WatchIssueIdentity(organizationID, issueID string) (string, error) {
	org, err := uuid.Parse(organizationID)
	if err != nil || org == uuid.Nil {
		return "", errors.New("linear: invalid organization ID")
	}
	issue, err := uuid.Parse(issueID)
	if err != nil || issue == uuid.Nil {
		return "", errors.New("linear: invalid issue ID")
	}
	return "linear:" + org.String() + ":" + issue.String(), nil
}

// issueWatchRow mirrors IssueWatch but stores the filter as a raw JSON string
// the way SQLite holds it. The store marshals/unmarshals at this boundary so
// service callers see a typed SearchFilter and never have to think about JSON.
type issueWatchRow struct {
	CreatedByUserID     string        `db:"created_by_user_id"`
	ID                  string        `db:"id"`
	WorkspaceID         string        `db:"workspace_id"`
	WorkflowID          string        `db:"workflow_id"`
	WorkflowStepID      string        `db:"workflow_step_id"`
	RepositoryID        string        `db:"repository_id"`
	BaseBranch          string        `db:"base_branch"`
	FilterJSON          string        `db:"filter_json"`
	AgentProfileID      string        `db:"agent_profile_id"`
	ExecutorProfileID   string        `db:"executor_profile_id"`
	Prompt              string        `db:"prompt"`
	Enabled             bool          `db:"enabled"`
	PollIntervalSeconds int           `db:"poll_interval_seconds"`
	MaxInflightTasks    sql.NullInt64 `db:"max_inflight_tasks"`
	SortBy              string        `db:"sort_by"`
	LastPolledAt        *time.Time    `db:"last_polled_at"`
	LastError           string        `db:"last_error"`
	LastErrorAt         *time.Time    `db:"last_error_at"`
	CreatedAt           time.Time     `db:"created_at"`
	UpdatedAt           time.Time     `db:"updated_at"`
}

func (r *issueWatchRow) toIssueWatch() (*IssueWatch, error) {
	var filter SearchFilter
	if r.FilterJSON != "" {
		if err := json.Unmarshal([]byte(r.FilterJSON), &filter); err != nil {
			return nil, fmt.Errorf("decode filter: %w", err)
		}
	}
	var maxInflight *int
	if r.MaxInflightTasks.Valid {
		v := int(r.MaxInflightTasks.Int64)
		maxInflight = &v
	}
	return &IssueWatch{
		CreatedByUserID:     r.CreatedByUserID,
		ID:                  r.ID,
		WorkspaceID:         r.WorkspaceID,
		WorkflowID:          r.WorkflowID,
		WorkflowStepID:      r.WorkflowStepID,
		RepositoryID:        r.RepositoryID,
		BaseBranch:          r.BaseBranch,
		Filter:              filter,
		AgentProfileID:      r.AgentProfileID,
		ExecutorProfileID:   r.ExecutorProfileID,
		Prompt:              r.Prompt,
		Enabled:             r.Enabled,
		PollIntervalSeconds: r.PollIntervalSeconds,
		MaxInflightTasks:    maxInflight,
		SortBy:              IssueSortBy(r.SortBy),
		LastPolledAt:        r.LastPolledAt,
		LastError:           r.LastError,
		LastErrorAt:         r.LastErrorAt,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}, nil
}

func encodeFilter(f SearchFilter) (string, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("encode filter: %w", err)
	}
	return string(b), nil
}

// issueWatchInsertColumns lists the writable column names in row-insert order.
// SELECTs use issueWatchSelectColumns which wraps the nullable last_error in
// COALESCE so older databases (pre-self-heal migration) read back as empty
// strings rather than NULL.
const issueWatchInsertColumns = `created_by_user_id, id, workspace_id, workflow_id, workflow_step_id,
	repository_id, base_branch, filter_json,
	agent_profile_id, executor_profile_id, prompt, enabled,
	poll_interval_seconds, max_inflight_tasks, sort_by, last_polled_at,
	last_error, last_error_at,
	created_at, updated_at`

const issueWatchSelectColumns = `created_by_user_id, id, workspace_id, workflow_id, workflow_step_id,
	COALESCE(repository_id, '') AS repository_id, COALESCE(base_branch, '') AS base_branch, filter_json,
	agent_profile_id, executor_profile_id, prompt, enabled,
	poll_interval_seconds, max_inflight_tasks, sort_by, last_polled_at,
	COALESCE(last_error, '') AS last_error, last_error_at,
	created_at, updated_at`

// CreateIssueWatch persists a new issue watch row. ID and timestamps are
// assigned here so callers can pass a partially-populated struct.
func (s *Store) CreateIssueWatch(ctx context.Context, w *IssueWatch) error {
	w.CreatedByUserID = executoridentity.CreatorUserID(ctx)
	if w.ID == "" {
		w.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now
	if w.PollIntervalSeconds <= 0 {
		w.PollIntervalSeconds = DefaultIssueWatchPollInterval
	}
	filterJSON, err := encodeFilter(w.Filter)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO linear_issue_watches (`+issueWatchInsertColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), w.CreatedByUserID,
		w.ID, w.WorkspaceID, w.WorkflowID, w.WorkflowStepID,
		w.RepositoryID, w.BaseBranch, filterJSON,
		w.AgentProfileID, w.ExecutorProfileID, w.Prompt, w.Enabled,
		w.PollIntervalSeconds, nullableInt(w.MaxInflightTasks), string(w.SortBy), w.LastPolledAt,
		w.LastError, w.LastErrorAt,
		w.CreatedAt, w.UpdatedAt)
	return err
}

// nullableInt converts a *int into a value suitable for a nullable SQL column.
// A nil pointer becomes a SQL NULL; a non-nil pointer becomes the underlying
// int. Used for max_inflight_tasks where NULL means "uncapped".
func nullableInt(v *int) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// GetIssueWatch returns a single watch by ID, or nil when no row matches.
func (s *Store) GetIssueWatch(ctx context.Context, id string) (*IssueWatch, error) {
	var row issueWatchRow
	err := s.ro.GetContext(ctx, &row, s.ro.Rebind(
		`SELECT `+issueWatchSelectColumns+` FROM linear_issue_watches WHERE id = ?`), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row.toIssueWatch()
}

// ListIssueWatches returns all watches configured for a workspace.
func (s *Store) ListIssueWatches(ctx context.Context, workspaceID string) ([]*IssueWatch, error) {
	var rows []issueWatchRow
	err := s.ro.SelectContext(ctx, &rows, s.ro.Rebind(
		`SELECT `+issueWatchSelectColumns+` FROM linear_issue_watches
		 WHERE workspace_id = ? ORDER BY created_at`), workspaceID)
	if err != nil {
		return nil, err
	}
	return materializeWatches(rows)
}

// ListAllIssueWatches returns every watch across all workspaces.
func (s *Store) ListAllIssueWatches(ctx context.Context) ([]*IssueWatch, error) {
	var rows []issueWatchRow
	err := s.ro.SelectContext(ctx, &rows, s.ro.Rebind(
		`SELECT `+issueWatchSelectColumns+` FROM linear_issue_watches ORDER BY workspace_id, created_at`))
	if err != nil {
		return nil, err
	}
	return materializeWatches(rows)
}

// ListEnabledIssueWatches returns every enabled watch across all workspaces,
// used by the poller to decide what to query each tick.
func (s *Store) ListEnabledIssueWatches(ctx context.Context) ([]*IssueWatch, error) {
	var rows []issueWatchRow
	err := s.ro.SelectContext(ctx, &rows, s.ro.Rebind(
		`SELECT `+issueWatchSelectColumns+` FROM linear_issue_watches
		 WHERE enabled = TRUE ORDER BY created_at`))
	if err != nil {
		return nil, err
	}
	return materializeWatches(rows)
}

func materializeWatches(rows []issueWatchRow) ([]*IssueWatch, error) {
	out := make([]*IssueWatch, 0, len(rows))
	for i := range rows {
		w, err := rows[i].toIssueWatch()
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// UpdateIssueWatch overwrites the mutable fields of an existing watch row.
// updated_at is bumped automatically; last_polled_at is preserved unless the
// caller explicitly sets it.
func (s *Store) UpdateIssueWatch(ctx context.Context, w *IssueWatch) error {
	w.UpdatedAt = time.Now().UTC()
	if w.PollIntervalSeconds <= 0 {
		w.PollIntervalSeconds = DefaultIssueWatchPollInterval
	}
	filterJSON, err := encodeFilter(w.Filter)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE linear_issue_watches SET workflow_id = ?, workflow_step_id = ?,
			repository_id = ?, base_branch = ?, filter_json = ?,
			agent_profile_id = ?, executor_profile_id = ?, prompt = ?,
			enabled = ?, poll_interval_seconds = ?, max_inflight_tasks = ?,
			sort_by = ?, last_polled_at = ?, updated_at = ?
		WHERE id = ?`),
		w.WorkflowID, w.WorkflowStepID,
		w.RepositoryID, w.BaseBranch, filterJSON,
		w.AgentProfileID, w.ExecutorProfileID, w.Prompt,
		w.Enabled, w.PollIntervalSeconds, nullableInt(w.MaxInflightTasks),
		string(w.SortBy), w.LastPolledAt, w.UpdatedAt, w.ID)
	return err
}

// UpdateIssueWatchLastPolled stamps the last-polled timestamp without touching
// the rest of the row.
func (s *Store) UpdateIssueWatchLastPolled(ctx context.Context, id string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(
		`UPDATE linear_issue_watches SET last_polled_at = ?, updated_at = ? WHERE id = ?`),
		t, time.Now().UTC(), id)
	return err
}

// DisableIssueWatchWithError is the self-heal write: it disables the watch
// and stamps a human-readable cause + timestamp so the settings UI can show
// a "disabled because ..." banner. Called by the orchestrator dispatch
// pipeline when the watcher's bound agent profile is detected as
// soft-deleted (see internal/agent/runtime/lifecycle/profile_resolver.go).
func (s *Store) DisableIssueWatchWithError(ctx context.Context, id, cause string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(
		`UPDATE linear_issue_watches
		   SET enabled = FALSE, last_error = ?, last_error_at = ?, updated_at = ?
		 WHERE id = ?`),
		cause, now, now, id)
	return err
}

// DeleteIssueWatch removes a watch and its dedup rows in a single transaction.
// The explicit child DELETE guards older databases where foreign_keys may not
// have been enabled at attach time.
func (s *Store) DeleteIssueWatch(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM linear_issue_watch_tasks WHERE issue_watch_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM linear_issue_watches WHERE id = ?`), id); err != nil {
		return err
	}
	return tx.Commit()
}

// ReserveIssueWatchTask keeps incomplete reservations eligible for idempotent retry.
func (s *Store) ReserveIssueWatchTask(ctx context.Context, watchID string, issue *LinearIssue) (bool, error) {
	if issue == nil || watchID == "" || issue.Identifier == "" || issue.URL == "" {
		return false, errors.New("linear: incomplete issue reservation")
	}
	if (issue.RecoveryPending && (issue.ReservationIdentifier == "" || issue.ReservationURL == "" || issue.ReservationID == "")) ||
		(!issue.RecoveryPending && (issue.ReservationIdentifier != "" || issue.ReservationURL != "")) {
		return false, errors.New("linear: incomplete recovery identity")
	}
	identity, err := WatchIssueIdentity(issue.OrganizationID, issue.ID)
	if err != nil {
		return false, err
	}
	identifier, issueURL := issue.Identifier, issue.URL
	if issue.ReservationIdentifier != "" {
		identifier, issueURL = issue.ReservationIdentifier, issue.ReservationURL
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if dialect.IsPostgres(s.db.DriverName()) {
		if _, err := tx.ExecContext(ctx, s.db.Rebind(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`), "linear-watch:"+watchID+":"+identity); err != nil {
			return false, err
		}
	}
	query := `SELECT id, issue_watch_id, issue_identifier, issue_url, task_id, created_at FROM linear_issue_watch_tasks WHERE issue_watch_id = ? AND id LIKE ?`
	if dialect.IsPostgres(s.db.DriverName()) {
		query += ` FOR UPDATE`
	}
	var rows []IssueWatchTask
	if err := tx.SelectContext(ctx, &rows, s.db.Rebind(query), watchID, identity+":%"); err != nil {
		return false, err
	}
	if len(rows) > 1 {
		return false, errors.New("linear: multiple reservations have the same issue identity")
	}
	if len(rows) == 0 {
		if issue.RecoveryPending {
			return false, errors.New("linear: recovery reservation no longer exists")
		}
		generation := uuid.NewString()
		for generation == watchID {
			generation = uuid.NewString()
		}
		row := IssueWatchTask{ID: identity + ":" + generation, IssueWatchID: watchID, IssueIdentifier: identifier, IssueURL: issueURL}
		if _, err := tx.ExecContext(ctx, s.db.Rebind(`INSERT INTO linear_issue_watch_tasks (id, issue_watch_id, issue_identifier, issue_url, task_id, created_at) VALUES (?, ?, ?, ?, '', ?)`), row.ID, watchID, identifier, issueURL, time.Now().UTC()); err != nil {
			return false, fmt.Errorf("linear: reserve issue identity: %w", err)
		}
		rows = append(rows, row)
	}
	row := rows[0]
	if row.IssueIdentifier != identifier || row.IssueURL != issueURL || (issue.RecoveryPending && row.ID != issue.ReservationID) {
		return false, errors.New("linear: issue reservation generation or source changed")
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	issue.ReservationID = row.ID
	return row.TaskID == "", nil
}

func (s *Store) ListIssueWatchReservations(ctx context.Context, watchID string) ([]IssueWatchTask, error) {
	var rows []IssueWatchTask
	err := s.ro.SelectContext(ctx, &rows, s.ro.Rebind(`SELECT id, issue_watch_id, issue_identifier, issue_url, task_id, created_at FROM linear_issue_watch_tasks WHERE issue_watch_id = ? ORDER BY created_at, id`), watchID)
	return rows, err
}

// AssignIssueWatchTaskID stamps the created task ID onto a previously-reserved
// dedup row.
func (s *Store) AssignIssueWatchTaskID(ctx context.Context, watchID, identifier, reservationID, taskID string) error {
	if taskID == "" || reservationID == "" {
		return errors.New("assign task ID: reservation and task IDs are required")
	}
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE linear_issue_watch_tasks SET task_id = ?
		WHERE id = ? AND issue_watch_id = ? AND issue_identifier = ? AND (task_id = '' OR task_id = ?)`),
		taskID, reservationID, watchID, identifier, taskID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("assign task ID: reservation missing or already attached for watch=%s issue=%s", watchID, identifier)
	}
	return nil
}

// ListIssueWatchTaskIDs returns every task_id recorded against a watch,
// including the empty-string sentinel reservations that never completed.
// Used by the reset flow to enumerate the tasks to cascade-delete.
func (s *Store) ListIssueWatchTaskIDs(ctx context.Context, watchID string) ([]string, error) {
	var ids []string
	err := s.ro.SelectContext(ctx, &ids, s.ro.Rebind(
		`SELECT task_id FROM linear_issue_watch_tasks WHERE issue_watch_id = ?`), watchID)
	return ids, err
}

// ResetIssueWatchState wipes the watch's dedup rows and nulls its
// last_polled_at in one transaction. Used by the reset flow after the
// cascade-delete loop so the next poll re-imports every currently-matching
// issue as if the watch were freshly created.
func (s *Store) ResetIssueWatchState(ctx context.Context, watchID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.db.Rebind(
		`DELETE FROM linear_issue_watch_tasks WHERE issue_watch_id = ?`), watchID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.db.Rebind(
		`UPDATE linear_issue_watches SET last_polled_at = NULL, updated_at = ? WHERE id = ?`),
		time.Now().UTC(), watchID); err != nil {
		return err
	}
	return tx.Commit()
}
