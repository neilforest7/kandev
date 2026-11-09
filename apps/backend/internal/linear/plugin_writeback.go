package linear

import (
	"context"
	"errors"
)

var ErrPluginWritebackUnsupported = errors.New("linear: linked issue writeback is unsupported by this client")

type pluginCommentClient interface {
	AddComment(context.Context, string, string, string) (string, error)
	GetCommentIssue(context.Context, string) (string, error)
}

// GetIssueForWorkspace reads one issue with the credentials owned by workspaceID.
func (s *Service) GetIssueForWorkspace(ctx context.Context, workspaceID, identifier string) (*LinearIssue, error) {
	client, err := s.clientFor(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return client.GetIssue(ctx, identifier)
}

// AddCommentForWorkspace writes one comment with a caller-chosen id through the
// workspace-owned client.
func (s *Service) AddCommentForWorkspace(ctx context.Context, workspaceID, issueID, body, commentID string) (string, error) {
	commenter, err := s.pluginCommenter(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return commenter.AddComment(ctx, issueID, body, commentID)
}

// GetCommentIssueForWorkspace returns the issue id holding commentID, or a 404
// *APIError when the comment does not exist.
func (s *Service) GetCommentIssueForWorkspace(ctx context.Context, workspaceID, commentID string) (string, error) {
	commenter, err := s.pluginCommenter(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return commenter.GetCommentIssue(ctx, commentID)
}

func (s *Service) pluginCommenter(ctx context.Context, workspaceID string) (pluginCommentClient, error) {
	client, err := s.clientFor(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	commenter, ok := client.(pluginCommentClient)
	if !ok {
		return nil, ErrPluginWritebackUnsupported
	}
	return commenter, nil
}

// SetIssueStateForWorkspace sets an issue state using workspace-owned credentials.
func (s *Service) SetIssueStateForWorkspace(ctx context.Context, workspaceID, issueID, stateID string) error {
	client, err := s.clientFor(ctx, workspaceID)
	if err != nil {
		return err
	}
	return client.SetIssueState(ctx, issueID, stateID)
}
