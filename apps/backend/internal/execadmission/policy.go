package execadmission

import (
	"errors"
	"fmt"
	"regexp"

	v1 "github.com/kandev/kandev/pkg/api/v1"
)

// Terminal states decide an admission before the bridge is asked. The set is
// shared with models.IsTerminalTaskState; a parity test keeps them equal.
var terminalStates = map[v1.TaskState]bool{
	v1.TaskStateCompleted: true,
	v1.TaskStateFailed:    true,
	v1.TaskStateCancelled: true,
}

// IsTerminalState reports whether a native task state ended the work.
func IsTerminalState(state v1.TaskState) bool {
	return terminalStates[state]
}

var planVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// NativeAncestor is one task in the host-derived root-to-current chain.
type NativeAncestor struct {
	ID       string
	State    v1.TaskState
	Archived bool
}

// Request is a host-derived admission request. Every field is read from native
// records; no model-supplied value reaches it.
type Request struct {
	OrganizationID string
	IssueID        string
	RootTaskID     string
	WorkspaceID    string
	TaskID         string
	SessionID      string
	Intent         string
	PlanVersion    string
	Profile        Profile
}

// Validate rejects a request the bridge would reject anyway, so the host never
// reports a malformed check as a bridge decision.
func (r Request) Validate() error {
	if err := r.validateIdentities(); err != nil {
		return err
	}
	if r.Intent != IntentImplementation && r.Intent != IntentInvestigation {
		return fmt.Errorf("execution admission intent %q is unsupported", r.Intent)
	}
	if r.PlanVersion != "" && !planVersionPattern.MatchString(r.PlanVersion) {
		return errors.New("execution admission plan version is malformed")
	}
	if err := r.Profile.Validate(); err != nil {
		return err
	}
	if r.Intent == IntentInvestigation && r.Profile.PermissionMode != "read-only" {
		return errors.New("investigation admission requires a read-only profile")
	}
	return nil
}

func (r Request) validateIdentities() error {
	for _, identity := range []struct{ name, value string }{
		{"organization", r.OrganizationID},
		{"issue", r.IssueID},
		{"root task", r.RootTaskID},
		{"workspace", r.WorkspaceID},
		{"task", r.TaskID},
		{"session", r.SessionID},
	} {
		if identity.value == "" {
			return fmt.Errorf("execution admission %s identity is unavailable", identity.name)
		}
	}
	if len(r.SessionID) > 128 || len(r.OrganizationID) > 128 || len(r.WorkspaceID) > 128 {
		return errors.New("execution admission identity is malformed")
	}
	return nil
}

// Validate rejects an incomplete effective profile.
func (p Profile) Validate() error {
	if p.ID == "" || p.Model == "" || p.PermissionMode == "" || p.ConfigDigest == "" {
		return errors.New("execution admission profile is incomplete")
	}
	return nil
}

// IntentFor picks the bridge intent from the private proof allowlist. A listed
// profile must be read-only; a read-only profile that is not listed stays an
// implementation session under its own approved roster entry, so intent is
// never inferred from the permission mode alone.
func IntentFor(profile Profile, investigationProfiles []string) (string, error) {
	for _, allowed := range investigationProfiles {
		if allowed != profile.ID {
			continue
		}
		if profile.PermissionMode != "read-only" {
			return "", errors.New("investigation proof profile is not read-only")
		}
		return IntentInvestigation, nil
	}
	return IntentImplementation, nil
}

// DenyTerminalAncestry rejects work whose source chain already ended. A
// completed, failed, canceled or archived ancestor cannot be re-admitted by a
// newer plan version.
func DenyTerminalAncestry(chain []NativeAncestor) error {
	if len(chain) == 0 {
		return errors.New("execution admission ancestry is unavailable")
	}
	for _, ancestor := range chain {
		if ancestor.ID == "" {
			return errors.New("execution admission ancestry is incomplete")
		}
		if ancestor.Archived || IsTerminalState(ancestor.State) {
			return fmt.Errorf("execution admission source task %q is no longer active", ancestor.ID)
		}
	}
	return nil
}

// DenyIntentChange rejects reusing one session for the other intent. Pinned is
// the intent the session's write-once pin recorded, empty before admission. An
// investigation session cannot escalate to implementation, and an approved
// implementation session cannot drop back to investigation.
func DenyIntentChange(pinned, requested string) error {
	if pinned == "" || pinned == requested {
		return nil
	}
	return fmt.Errorf("execution admission intent changed from %q to %q; a new session is required", pinned, requested)
}

// DenyProfileDrift rejects a session pinned for one profile being admitted as
// another. Mutable task metadata and model-editable inputs are never a source.
func DenyProfileDrift(pinned Profile, requested Profile) error {
	if pinned.ID == "" {
		return nil
	}
	if pinned != requested {
		return errors.New("execution admission profile changed; a new session is required")
	}
	return nil
}
