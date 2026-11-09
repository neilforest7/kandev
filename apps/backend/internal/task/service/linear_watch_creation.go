package service

import (
	"context"
	"errors"
	"strings"

	"github.com/kandev/kandev/internal/task/models"
)

// MarkLinearWatchCreationComplete records proof only after CreateTask's synchronous work returned successfully.
func (s *Service) MarkLinearWatchCreationComplete(ctx context.Context, taskID, externalID string) error {
	if !strings.HasPrefix(externalID, ReservedExternalIDPrefixLinearWatch) {
		return errors.New("linear watch creation identity is invalid")
	}
	task, err := s.tasks.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || task.ExternalID != externalID || task.ExternalIDSettledAt != nil {
		return errors.New("linear watch creation identity changed")
	}
	writer, ok := s.tasks.(interface {
		SetTaskMetadataKey(context.Context, string, string, interface{}) error
	})
	if !ok {
		return errors.New("linear watch creation receipt storage unavailable")
	}
	if err := writer.SetTaskMetadataKey(ctx, taskID, models.MetaKeyLinearWatchCreationComplete, externalID); err != nil {
		return err
	}
	task, err = s.tasks.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || task.ExternalID != externalID || linearWatchCreationReceipt(task) != externalID {
		return errors.New("linear watch creation receipt not confirmed")
	}
	s.PublishTaskUpdated(ctx, task)
	return nil
}

// SettleLinearWatchCreation refuses recovery until the durable completion receipt exists.
func (s *Service) SettleLinearWatchCreation(ctx context.Context, taskID, externalID string) error {
	task, err := s.tasks.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil || task.ExternalID != externalID || !strings.HasPrefix(externalID, ReservedExternalIDPrefixLinearWatch) {
		return errors.New("linear watch creation identity changed")
	}
	if task.ExternalIDSettledAt != nil {
		return nil
	}
	if linearWatchCreationReceipt(task) != externalID {
		return errors.New("linear watch creation is unsettled and requires reconciliation")
	}
	settled, survivor, err := s.SettleExternalID(ctx, taskID, externalID)
	if err != nil {
		return err
	}
	if !settled && (survivor == nil || survivor.ExternalID != externalID || survivor.ExternalIDSettledAt == nil) {
		return errors.New("linear watch creation identity was not settled")
	}
	return nil
}

func linearWatchCreationReceipt(task *models.Task) string {
	receipt, _ := task.Metadata[models.MetaKeyLinearWatchCreationComplete].(string)
	return receipt
}
