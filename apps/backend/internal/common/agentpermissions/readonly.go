// Package agentpermissions identifies provider modes with Host-enforced restrictions.
package agentpermissions

// IsReadOnly keeps native read-only modes and the restricted Claude Plan mode aligned.
func IsReadOnly(agent, mode string) bool {
	return mode == "read-only" || (agent == "claude-acp" && mode == "plan")
}
