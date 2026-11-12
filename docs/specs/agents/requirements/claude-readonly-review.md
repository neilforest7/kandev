---
status: active
system: agents
---

# Claude Read-only Review

## Overview

The agent system owns provider permissions. A Claude reviewer must read a
prepared diff and source files without tools that can mutate its workspace.

## Requirements

### REQ-AGENTS-CLAUDE-READONLY-001: Restricted Claude review

#### Acceptance criteria

- **AC-AGENTS-CLAUDE-READONLY-001.1:** A `claude-acp` profile using Plan shall
  receive Host read-only MCP binding and execution admission. Its native Plan
  mode shall be confirmed before the first prompt.
- **AC-AGENTS-CLAUDE-READONLY-001.2:** New and restored restricted sessions shall
  expose only Read, Grep, Glob and Host-injected read-only MCP tools. User,
  project and local settings shall not supply permissions, hooks or tools.
- **AC-AGENTS-CLAUDE-READONLY-001.3:** Native non-question permission requests
  shall be rejected even with automatic approval enabled. Changes that leave
  the restricted mode shall require a new session.
- **AC-AGENTS-CLAUDE-READONLY-001.4:** Codex, Pi and other Claude modes shall
  retain their current permissions. Restricted Claude shall reject passthrough
  and external MCP attachments.

## Exclusions

Live runtime deployment and acceptance belong to the operator. The main agent
prepares the diff file; the reviewer does not run Git or a shell. Runtime-owned
transcript and cache writes are outside the workspace-tool restriction.
