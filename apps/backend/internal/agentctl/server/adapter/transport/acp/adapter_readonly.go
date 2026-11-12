package acp

// sessionMeta delivers native restrictions on both creation and restore.
func (a *Adapter) sessionMeta() map[string]any {
	meta := a.promptSuggestionSessionMeta()
	if a.cfg.AgentID != claudeAgentID || !a.cfg.ReadOnly {
		return meta
	}
	if meta == nil {
		meta = make(map[string]any)
	}
	claude, _ := meta["claudeCode"].(map[string]any)
	if claude == nil {
		claude = make(map[string]any)
		meta["claudeCode"] = claude
	}
	options, _ := claude["options"].(map[string]any)
	if options == nil {
		options = make(map[string]any)
		claude["options"] = options
	}
	// tools removes native capabilities; allowedTools would only preapprove them.
	options["tools"] = []string{"Read", "Grep", "Glob"}
	options["settingSources"] = []string{}
	options["strictMcpConfig"] = true
	options["allowDangerouslySkipPermissions"] = false
	return meta
}
