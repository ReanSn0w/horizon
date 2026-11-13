# Subagent tools

`horizon-subagent` is a short-lived client of [daemon](DAEMON.md), not the child
process itself. Children run normal Horizon turns in separate sessions, in the
parent's workspace. They receive an explicit task, not a copy of parent history.
Files are shared: there is no automatic worktree, merge, sandbox or live steering.

Enable with `agent_plugins: [subagent]` after installing daemon and subagent.
Start daemon explicitly. An enabled but unavailable/incompatible daemon fails
preparation, as for other broken explicit extensions; it is never started by
metadata, describe, context, compact or help. Disabled extensions do not connect.
Describe/context perform a bounded handshake and make no persistent changes.

## Tool contract

All object arguments are strict, all listed fields required. Empty strings select
optional identifiers where documented. Host supplies home/workspace, effective
access, session/turn/call and operation ID, never model arguments.

| Tool | Arguments | Result |
| --- | --- | --- |
| `subagent__spawn` | message, access (`read`, `write`, `full`) | Accepted task/child ID and queued state |
| `subagent__list` | cursor (0 initially) | Up to 32 tasks, next_cursor, event_cursor |
| `subagent__read` | task_id, operation_id, offset | Task state, result page, next_offset, truncated |
| `subagent__send` | task_id, message | New queued turn in the child's same session |
| `subagent__wait` | cursor, wait_ms (0..30000) | Up to 32 status events and next cursor |
| `subagent__cancel` | task_id, tree | Current task state after child/subtree cancellation |

Read accepts either task_id or operation_id, with the other empty. operation_id
resolves an earlier mutation with a lost response, then reads its task. Offset
starts at 0; next_offset=0 means complete. Result pages are at most 8192 bytes at
UTF-8 boundaries. List cursor and event cursor are separate sequences. On expired
event cursor list again for a fresh snapshot. Read does not mutate queued state.

Spawn/send/cancel have write_home effect; list/read/wait have read effect. read
cannot create even a read-only child because queue and session creation write home.
Children cannot exceed the caller's access. Send intersects the existing child's
access with the current caller's; daemon enforces this again and injects inherited
access into each process. The caller may only see/manage children of its own parent
session. Cancellation with tree includes descendants. Other sessions' tasks are
not exposed. Local clients are trusted user processes, not an OS security boundary.

A call uses the host operation_id for deduplication. Errors after sending a request
may report outcome_unknown, with that ID and recovery instructions. Do not repeat
a mutation using a new ID before checking read(operation_id). Host pending/known/
unknown journal remains authoritative for whether a plugin invocation completed.

Wait returns after at most 30 seconds (under the 60-second host tool limit).
Disconnecting or cancelling wait does not cancel accepted tasks. Parent completion
does not cancel children; a later turn in the same parent session can collect them.
Send queues the next turn, including for a completed child. Execution capacity is
reserved per depth; explicit depth/queue/child limits prevent unbounded delegation.

## Setup

```sh
horizon manager plugins install agent
horizon manager plugins install subagent
horizon agent start
# In another terminal:
horizon subagent status
```

Add `subagent` to the existing `agent_plugins` list in config.yaml. Installation
never enables it automatically. `make install-subagent` is available for local
development. Use an isolated temporary home for tests. Stop daemon before updates.
