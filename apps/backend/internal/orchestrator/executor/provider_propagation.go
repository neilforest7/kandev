package executor

import (
	"context"
	"fmt"

	mcpproviders "github.com/kandev/kandev/internal/mcp/providers"
)

// deriveMCPProviders returns the normalized provider capability union for the
// task's resolved repositories. It deliberately reads only the persisted
// repository provider, never a filesystem remote, so launch behavior is stable
// for local checkouts and provider-backed clones alike.
func deriveMCPProviders(repositories []*repoInfo) []string {
	values := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		if repository == nil || repository.Repository == nil {
			continue
		}
		values = append(values, repository.Repository.Provider)
	}
	return mcpproviders.Normalize(values)
}

// resolveTaskMCPProviders returns the normalized provider capability union for
// the task's persisted repositories. It is the same authority deriveMCPProviders
// uses, resolved from the task rows alone so a pre-launch roster can hash the
// value the launch path will attach. A repository that cannot be loaded refuses
// instead of silently shrinking the union.
func (e *Executor) resolveTaskMCPProviders(ctx context.Context, taskID string) ([]string, error) {
	taskRepos, err := e.repo.ListTaskRepositories(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task repositories for MCP providers: %w", err)
	}
	values := make([]string, 0, len(taskRepos))
	for _, taskRepo := range taskRepos {
		if taskRepo == nil {
			return nil, fmt.Errorf("task repository for MCP providers is unavailable")
		}
		repository, err := e.repo.GetRepository(ctx, taskRepo.RepositoryID)
		if err != nil {
			return nil, fmt.Errorf("load repository %q for MCP providers: %w", taskRepo.RepositoryID, err)
		}
		if repository == nil {
			return nil, fmt.Errorf("repository %q for MCP providers is unavailable", taskRepo.RepositoryID)
		}
		values = append(values, repository.Provider)
	}
	return mcpproviders.Normalize(values), nil
}
