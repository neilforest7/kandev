---
created: 2026-11-12
status: implemented
requirements:
  - REQ-AGENTS-CLAUDE-READONLY-001
system_design:
  - ../../specs/agents/system-design/claude-readonly-review.md
legacy_specs: []
---

# Implementation Plan: Claude Read-only Review

## Scope and approach

Implement the explicit Claude Plan restriction and hard native tool selection
as one vertical change. Reuse existing MCP binding, strict mode confirmation,
permission refusal and execution-roster fingerprints. No frontend changes,
dependencies, live writes, deployments, pushes or delegated workers.

| Provider | Session policy | Evidence |
| --- | --- | --- |
| Claude ACP Plan | Hard read tools and Host read-only MCP | Mapping, transport and refusal tests; live preflight pending |
| Other Claude modes | Existing behavior | Negative mapping and metadata assertions |
| Codex/Pi | Existing behavior | Negative mapping and metadata assertions |

## Work orders

- [x] [Task 01: Bind and enforce Claude read-only review](task-01-enforce-review.md)

## Verification

Run the work order's focused Go checks using pinned Go and build_guard.py.
Run specification catalog validation and specification lint after edits.
Commit the verified change locally with Conventional Commits. The operator
owns runtime rollout and real write-refusal acceptance.

## Risks

All Claude Plan profiles become restricted in this fork. Disabling settings
may remove provider configuration required for authentication. The three read
tools must really be available with the installed SDK; no Bash fallback is
permitted. `TestPluginExecutorPackagedRecovery` is a brief-reported existing
failure and is not evidence against this change unless reproduced here.

## Verification results

The work order's five targeted Go test commands, nine-package `go vet`,
changed-package Go lint, specification catalog validation, specification lint,
and `git diff --check` passed on 2026-11-12 using pinned Go 1.26. Tests cover
mapping, native mode confirmation, binding, new/load/resume metadata and native
permission refusal. Real Claude execution remains operator preflight work.

Local commit is blocked: the session cannot create the shared worktree's
`index.lock`. No push or candidate binary build occurred. The leader cancelled
the proposed candidate build and owns review, commit and rollout.
