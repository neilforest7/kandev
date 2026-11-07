package plugins

import (
	"testing"
	"time"

	"github.com/kandev/kandev/internal/task/models"
)

// The plugin layer keys its waiting ledger on the opaque interaction revision,
// so any same-second change, including a status change, must alter the DTO.
func TestInteractionDTOKeepsSubSecondUpdatesInRevision(t *testing.T) {
	pending := pendingPermissionInteraction()
	pending.UpdatedAt = time.Date(2026, 9, 26, 10, 0, 0, 100_000_000, time.UTC)

	reopened := *pending
	reopened.UpdatedAt = time.Date(2026, 9, 26, 10, 0, 0, 900_000_000, time.UTC)

	answered := *pending
	answered.Status = models.InteractionStatusApproved
	answered.UpdatedAt = reopened.UpdatedAt

	pendingDTO, reopenedDTO, answeredDTO := interactionModelToDTO(pending), interactionModelToDTO(&reopened), interactionModelToDTO(&answered)
	if pendingDTO.UpdatedAt == reopenedDTO.UpdatedAt {
		t.Fatalf("same-second updates collapsed to %q", pendingDTO.UpdatedAt)
	}
	if digestPublicValue(pendingDTO) == digestPublicValue(reopenedDTO) {
		t.Fatal("same-second pending update did not change the interaction revision")
	}
	if pendingDTO.UpdatedAt == answeredDTO.UpdatedAt || digestPublicValue(pendingDTO) == digestPublicValue(answeredDTO) {
		t.Fatal("same-second status change did not change the interaction revision")
	}
}
