package lifecycle

import (
	"context"
)

func (sm *SessionManager) admitExecutorPrompt(ctx context.Context, execution *AgentExecution) (context.Context, error) {
	if err := execution.validateBuiltinIntent(ctx); err != nil {
		return ctx, err
	}
	if sm.executorAdmission == nil {
		return ctx, nil
	}
	metadata := execution.MetadataSnapshot()
	ctx, err := sm.executorAdmission(ctx, execution.SessionID, getMetadataString(metadata, "executor_id"), getMetadataString(metadata, "executor_profile_id"))
	if err != nil {
		return ctx, err
	}
	if sm.admitExecution != nil {
		if err := sm.admitExecution(ctx, execution); err != nil {
			return ctx, err
		}
	}
	return ctx, nil
}
