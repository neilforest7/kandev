package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrSourceLinkNotFound  = errors.New("linear: task has no linked issue")
	ErrSourceLinkAmbiguous = errors.New("linear: task has multiple linked issues")
)

// TaskIssueLink is the host-owned association between a task and its Linear issue.
type TaskIssueLink struct {
	WorkspaceID     string
	OrganizationID  string
	IssueID         string
	IssueIdentifier string
	IssueURL        string
}

// GetTaskIssueLink resolves only issues attached through a persisted Linear watch.
func (s *Service) GetTaskIssueLink(ctx context.Context, taskID string) (*TaskIssueLink, error) {
	return s.store.GetTaskIssueLink(ctx, taskID)
}

func (s *Store) GetTaskIssueLink(ctx context.Context, taskID string) (*TaskIssueLink, error) {
	if taskID == "" {
		return nil, ErrSourceLinkNotFound
	}
	rows, err := s.ro.QueryContext(ctx, s.ro.Rebind(`
		SELECT DISTINCT w.workspace_id, t.issue_identifier, t.issue_url, t.id, t.issue_watch_id
		FROM linear_issue_watch_tasks t
		JOIN linear_issue_watches w ON w.id = t.issue_watch_id
		WHERE t.task_id = ?
		ORDER BY w.workspace_id, t.issue_identifier, t.issue_url, t.id, t.issue_watch_id
	`), taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var link *TaskIssueLink
	for rows.Next() {
		candidate := &TaskIssueLink{}
		var identity, watchID string
		if err := rows.Scan(&candidate.WorkspaceID, &candidate.IssueIdentifier, &candidate.IssueURL, &identity, &watchID); err != nil {
			return nil, err
		}
		parts := strings.SplitN(identity, ":", 4)
		if len(parts) == 4 && parts[0] == "linear" {
			generation, genErr := uuid.Parse(parts[3])
			if canonical, err := WatchIssueIdentity(parts[1], parts[2]); err == nil && genErr == nil && generation != uuid.Nil && generation.String() != watchID && canonical+":"+generation.String() == identity {
				candidate.OrganizationID = parts[1]
				candidate.IssueID = parts[2]
			}
		}
		if strings.HasPrefix(identity, "linear:") && candidate.IssueID == "" {
			return nil, errors.New("linear: invalid source association identity")
		}
		if link != nil {
			return nil, ErrSourceLinkAmbiguous
		}
		link = candidate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if link == nil {
		return nil, ErrSourceLinkNotFound
	}
	return link, nil
}

// ValidateTaskIssueLink checks that the supplied identity still matches the
// persisted association in this workspace.
func (s *Service) ValidateTaskIssueLink(ctx context.Context, taskID, workspaceID, issueIdentifier string) error {
	link, err := s.GetTaskIssueLink(ctx, taskID)
	if err != nil {
		return err
	}
	if link.WorkspaceID != workspaceID || link.IssueIdentifier != issueIdentifier {
		return fmt.Errorf("%w: task source association changed", ErrSourceLinkNotFound)
	}
	return nil
}
