# Daemon protocol v1

`horizon daemon start` runs a foreground supervisor for one normalized home.
`status` performs a handshake; `stop` requests shutdown. Installation does not
start services. Stop the daemon before manager updates: the shared maintenance
lock protects its running binary set, and exclusive maintenance fails promptly.
The existing `horizon telegram` remains independent until its separate migration.

The endpoint is `<home>/run/daemon.sock` (0600), protected by a persistent
`daemon.lock` flock in the same private directory (0700). A stale socket may
only be removed while holding that lock and after a failed connection probe.
Regular files and symlinks are never removed as stale sockets. Long Unix socket
paths fail explicitly. State is `<home>/daemon/state.json`, version 2, atomically
replaced with mode 0600. Version 1 remains readable. Unknown versions fail on
startup. No session JSON is edited by daemon.

## Transport

One newline-delimited JSON request and response per connection. Version is 1;
request IDs are echoed. Maximum frame is 1 MiB, at most 64 concurrent clients.
Requests have `version`, `id`, `method`, and optional `operation_id`, `caller`,
`task_id`, `message`, `access`, `cursor`, `offset`, `wait_ms`, `tree`, `route`.
Caller identifies kind (subagent/adapter), source, workspace, parent session,
access and an optional adapter credential. These are trusted local client fields,
not model arguments. This is not an OS sandbox against the same user.

`hello` returns protocol version and capabilities. `submit`, `send`, `cancel`
and `ack` require a nonempty operation ID. Source-scoped IDs deduplicate retries;
changing a request under an existing ID is a conflict. A lost response after
sending a mutation is an unknown outcome; query by operation ID before retrying.
`list`, `read`, `wait`, `lookup` are read-only. `stop` is administrative.
Errors have stable codes and a message; responses echo request ID and version.
Wait is capped at 30 seconds, other requests have bounded socket deadlines.

## Tasks and authority

A task ID identifies one queued turn. A child ID is its first task ID; `send`
creates a later task in that child's same session. Parent identity is workspace
plus session. An external Horizon can submit children by passing its host-selected
parent identity; the daemon never acquires or resumes the parent's active session.
A daemon-owned parent's depth and access are resolved from its recorded session.
Subagent clients can only see/manage their own direct children (tree cancellation
also traverses descendants). Adapter clients are scoped to their registered source
and routes. A child always uses the parent's workspace, a new session, and access
no broader than the current caller. Each send also intersects existing child access
with current caller access. read callers cannot submit/send/cancel/ack.

States: queued → running → succeeded/failed/cancelled/unknown; queued may be
cancelled before starting. Restart converts running to unknown, never retries it.
Queued turns behind an unknown turn stay blocked pending explicit cancellation of
those queued turns or operator investigation. No persisted PID is blindly killed.
Sessions serialize; independent sessions run concurrently. Top-level adapter
work has its own `plugins.daemon.max_active_adapter_dialogs` capacity (default
2, range 1–128). It runs one task per source and route at a time, using the
workspace for older adapter requests without a route. The scheduler prefers the
dialog least recently dispatched and keeps queued order within each dialog.
Subagent work has separate capacity per delegation depth, so waiting ancestors
do not occupy descendants' slots. Active adapter input uses the same slot as
its running turn. Depth, children per parent and pending queue are bounded. Reaching depth limit
fails immediately. There is no guarantee of progress for cyclic peer dependencies.

Ordinary `submit` and `send` put messages in a new turn. Session creation and
resume use the Horizon CLI. Cancellation targets a task/child or explicit subtree;
closing a wait or finishing the parent turn does not cancel children. Shutdown
cancels process groups with TERM then KILL; detached descendants are not covered.
Failures to persist dispatch prevent execution; failures to persist completion
stop scheduling instead of retrying. Horizon retains its own session locks.

Events have monotonic cursors; the most recent 1024 events are retained, older
cursors return `cursor_expired`. Pages contain at most 32 events/tasks. Results
are paged separately by byte offset (at UTF-8 boundaries). Status events contain
only task identity, state and stage, never model reasoning or tool payloads.
Execution completion and adapter delivery acknowledgment are separate records.
Task and operation records are retained until explicit offline administration;
there is no automatic deletion of deduplication records.

## Active input

When `hello` advertises `active-input`, a registered adapter can submit with
`delivery: "active"`, its stable `route`, a nonempty `author`, `message` and
`operation_id`. With delivery omitted, `submit` and `send` keep their original
behavior. Active delivery is adapter-only and uses `submit`. The pair (source,
route) identifies one dialog with a fixed normalized workspace and one daemon
child/session. An access change requires a new turn. A different access level
cannot be mixed into pending input. The adapter authorizes the actual sender
before issuing the request, including the requested access.

The daemon persists every accepted message separately. The task summary from
`submit` and `lookup` includes an `input` receipt with its ID, monotonically
increasing sequence, source, route and status. Status `stored` confirms durable
acceptance by the daemon; `included` confirms Horizon has persisted the input
in the reported session and turn. The receipt carries neither message text nor
author. Neither acknowledgment means the model or the adapter has answered.
Use `lookup` with the original operation ID after a lost response; retry only
when the operation is confirmed absent, with the same fields. Deduplication is
scoped to the authenticated adapter source.

Each message is at most 128 KiB, and at most 32 unconfirmed messages may be
pending per dialog. Excess pending input fails with `input_full`; no messages
are removed to accept new ones. The normal pending-task limit still applies
when admission needs a new task. A queued dialog batches its messages in order
into one run. A running turn accepts input at safe boundaries between requests
and complete tool results. It atomically closes input when ready to finish.
An arrival after close waits for the next turn. Arrivals never interrupt a live
Responses stream or a tool.

After restart, an interrupted task is unknown and its dialog stays blocked.
An adapter operator can explicitly resolve that task with `resolve_input` and
a new operation ID after investigating the unknown outcome. The task retains
its unknown status. Unconfirmed input is eligible for a later turn, and Horizon
checks its session journal for already included IDs before passing input to
the model. The interrupted process and any tool effects are not replayed.

The process channel is bound by the daemon to one task, normalized workspace,
session and route. Its short-lived credential is inherited by Horizon through
a private descriptor. Message content and authors stay out of diagnostic status
events. A persistence or transport error never constitutes acknowledgment.

## Adapter contract

Only immediate executable `agent-<name>` entries in home/bin are candidates;
only names explicitly enabled in `plugins.daemon.adapters` are started.
`agent-plugin-metadata` returns protocol_version 1, version, description and
capabilities. `agent-plugin-config-template` optionally follows the existing
config-template envelope, using an independently named `plugins.agent-<name>`
section. Neither operation may read credentials or contact external services.

`agent-run` is a supervised process. It receives HORIZON_HOME,
HORIZON_DAEMON_SOCKET, HORIZON_ADAPTER_NAME and a per-start credential in its
private environment, never command arguments. It handshakes over the socket
with caller.kind=adapter and caller.source=<name>. Requests are bound to this
credential; route is supplied on submit and fixed for subsequent turns.
The adapter validates channel-specific permissions before submission. It consumes
status/results through list/read/wait and marks confirmed delivery with ack.
A failed/unknown delivery is not blindly repeated. User-facing messages can be
returned as task results; pausing the core for interactive approval is not added.
Restart is delayed with bounded backoff; stopping daemon stops all adapters.

## Installation

```sh
horizon manager plugins install daemon
horizon daemon start
# In another terminal:
horizon daemon status
horizon daemon stop
```

Manager catalog entries optionally use `kind: "adapter"` (default `cli`).
Adapter `name: "telegram"` has binary `agent-telegram` and manager identity
`agent:telegram`, distinct from CLI plugin `telegram` / `horizon-telegram`.
Source remains `plugins/<directory>/plugin.json`. Install/remove use
`horizon manager plugins install|remove agent:NAME`. Config templates belong to
`plugins.agent-NAME`; enabling is explicit via `plugins.daemon.adapters: [NAME]`.
Removal disables that adapter while preserving settings and data. Manager stages
and validates both kinds before replacement; transaction rollback covers both.

## Timer adapter

`agent-timer` uses `submit` with `mode: "fresh"`, `source_key` set to the timer
ID and optional RFC 3339 `deadline`. The adapter supplies the normalized
workspace as caller workspace, the chosen run access and the Markdown message.
A fresh submission from the timer source has no parent session, prior child,
dialog route or active-turn input. Its operation ID is derived from timer ID and
scheduled instant. Queued and active tasks count against daemon capacity.
Historical fresh children do not count against `max_children`. There is at most
one queued or active task for each timer ID, and timer tasks in the same
workspace run in sequence. Subagent execution remains available while a timer
turn runs.

A deadline is exclusive. An expired queued task becomes `skipped` without
creating a session or contacting the model. `cancel` with `queued_only: true`
can cancel one queued timer task. If dispatch acquires the queue lock first,
cancellation leaves the running task and its message intact. Timer records
and recovery are documented in [Timer](../plugins/agent-timer/README.md).
