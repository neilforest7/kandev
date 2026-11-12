---
id: TASK-CLAUDE-READONLY-01
title: Bind and enforce Claude read-only review
status: done
wave: 1
depends_on: []
plan: plan.md
requirements:
  - REQ-AGENTS-CLAUDE-READONLY-001
acceptance_criteria:
  - AC-AGENTS-CLAUDE-READONLY-001.1
  - AC-AGENTS-CLAUDE-READONLY-001.2
  - AC-AGENTS-CLAUDE-READONLY-001.3
  - AC-AGENTS-CLAUDE-READONLY-001.4
system_design:
  - ../../specs/agents/system-design/claude-readonly-review.md
---

# Bind and enforce Claude read-only review

## Scope

Add the shared predicate and use it at every existing exact native read-only
mode comparison. Preserve native Plan bootstrap confirmation. Propagate the
bound restriction into all three ACP session metadata paths. Keep injected
Kandev read-only approval and human questions within existing policy.

Owned source: `internal/common/agentpermissions`,
`internal/orchestrator/executor/executor_builtin_mcp.go`,
`internal/execadmission/profile.go`,
`internal/agent/runtime/lifecycle/{builtin_mcp_admission,manager_launch}.go`,
`internal/agentctl/server/{api/agent,process/manager,adapter/adapter}.go`,
`internal/agentctl/server/adapter/transport/{shared/config,acp/adapter_session}.go`,
and the new ACP session metadata helper. Tests live alongside these boundaries.

## Acceptance

1. Mapping and real binding/admission paths agree for Claude Plan; unchanged
   provider/mode cases stay unrestricted.
2. New/load/resume requests carry the exact tool/settings/MCP restriction,
   including alongside suggestion metadata, with no provider leakage.
3. Native write permission requests are refused; attempts to leave the bound
   restriction fail. Existing mode-confirmation failure checks still pass.

## Verification commands

From `apps/backend`, with pinned Go on PATH, wrap Go commands using
`python3 /Users/forest/Projects/Coding/code-review-workflow/kandev-linear-integration/scripts/build_guard.py --`:

```sh
go test -trimpath ./internal/common/agentpermissions ./internal/execadmission -count=1
go test -trimpath ./internal/orchestrator/executor -run 'TestBuiltinProfileReadOnly|TestClaudeReadOnly' -count=1
go test -trimpath ./internal/agent/runtime/lifecycle -run 'TestClaudeReadOnly|TestReadOnly|TestApplyExplicitSessionMode|TestLegacyConfirmedModeLifecycle|TestInitializeAndPromptWithLayers_(AuggieTaskRejectsUnappliedMode|AppliesOnlyWinningModeBeforePrompt|ReappliesModeAfterResumeBeforePrompt)' -count=1
go test -trimpath ./internal/agentctl/server/adapter/transport/acp -run 'TestClaudeReadOnly|TestPromptSuggestionSessionConfiguration' -count=1
go test -trimpath ./internal/agentctl/server/process ./internal/agentctl/server/api -run 'TestReadOnly|TestClaudeReadOnly' -count=1
go vet -trimpath ./internal/common/agentpermissions ./internal/execadmission ./internal/orchestrator/executor ./internal/agent/runtime/lifecycle ./internal/agentctl/server/adapter ./internal/agentctl/server/adapter/transport/shared ./internal/agentctl/server/adapter/transport/acp ./internal/agentctl/server/process ./internal/agentctl/server/api
```

From repo root, run `python3 scripts/list-docs.py validate` and
`python3 scripts/lint-spec-files.py --all`; lint changed Go files through
`bash scripts/lint-go-changed <changed-go-files>`.

## Exclusions and risks

No live Host, Linear or profile writes, full builds, push or provider calls.
Real preflight must prove the actual tool inventory, refusals, model/effort,
read functionality, MCP filtering, terminal child completion and collection.

## Results

All five targeted Go test commands above passed on 2026-11-12. The explicit
nine-package vet command passed. Changed-package Go lint passed after replacing
a duplicate Claude identifier literal with the existing `claudeAgentID`
constant; the affected ACP tests and nine-package vet passed again afterward.
Specification checks and `git diff --check` passed.

Regression evidence before correction: Claude Plan lacked a read-only MCP
binding and effective roster classification; its launch was not restricted;
the existing API mode guard rejected Plan on a read-only session. The new
adapter tests initially failed to compile without the restriction carrier.

Pinned pnpm 9 workspace installation succeeded. The shared Go cache was denied,
so checks used an owned `/tmp` cache and the required build guard. One
intermediate run encountered a vet import error while API source was changing;
the final stable-source checks passed. No real-provider pass is claimed.

Commit is blocked by permission denial creating the shared Git `index.lock`.
The leader owns the final commit and cancelled the candidate binary build.
