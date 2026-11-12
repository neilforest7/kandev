---
status: current
system: agents
requirements:
  - REQ-AGENTS-CLAUDE-READONLY-001
---

# Claude Read-only Review System Design

## Requirement mapping

| Requirement | Design |
| --- | --- |
| REQ-AGENTS-CLAUDE-READONLY-001 | Host binding and native tool policy below |

## Host binding

Use one predicate in `internal/common/agentpermissions` for native `read-only`
and the explicit pair `claude-acp` plus `plan`. Apply it in orchestrator MCP
preparation, lifecycle launch and retained-binding validation, agentctl mode
admission, and execution admission's effective permission mode. Digest inputs
retain the native `plan` string and the resolved read-only MCP grants.

The profile remains native `plan`; the existing strict mode confirmation
continues to verify that exact value before prompting. All Claude Plan profiles
in this fork acquire this restriction. No other provider's Plan mode does.

## Native tool policy

Carry the bound MCP read-only bit through process Manager, adapter.Config and
shared.Config. For restricted `claude-acp`, merge Host-owned metadata into all
session/new, session/load and session/resume requests, preserving prompt
suggestion metadata:

```json
{"claudeCode":{"options":{"tools":["Read","Grep","Glob"],"settingSources":[],"strictMcpConfig":true,"allowDangerouslySkipPermissions":false}}}
```

These are SDK `tools`, not `allowedTools`: unavailable native tools cannot be
granted by the permission classifier. Strict MCP configuration admits only the
Host-delivered servers, whose existing read-only binding filters their surface.
Existing agentctl permission rejection remains the final callback policy.

## Source evidence and failure boundaries

Pinned bridge `0.81.2`, commit
`5dbb453c63a89746627799b2b06b31ba01a1b674`, forwards these options. Its
`src/acp-agent.ts:8380` selects the explicit tools array and `:8472` spreads
client options after default settingSources. `:1210` classifies tools,
settingSources and strictMcpConfig changes as requiring query reconstruction on
restore. SDK `0.3.280` documents the tools array as the available built-in set.

Source inspection supports this design; real provider write refusal remains an
operator preflight requirement. Installed versions must be recorded. Host
mock/transport tests cannot prove upstream tool enforcement or absence of
unexpected tools. Managed machine policy and provider credential behavior must
be inspected during preflight if disabling settings changes authentication.

## Persistence and recovery

Reuse the bound MCP profile and profile mode; add no schema or settings files.
Retained unrestricted processes cannot be promoted into restricted sessions.
Use a fresh reviewer session after deployment. Preserve denial of passthrough,
external MCP servers, model changes and unrestricted mode changes.
