# Session discovery and history

`horizon sessions list` lists sessions in the current workspace. `--workspace
PATH` selects another existing workspace. `--all` selects all workspaces in the
chosen `HORIZON_HOME`; it cannot be combined with `--workspace`. No command
searches another home or the whole computer.

`--since DURATION` accepts a positive Go duration, such as `24h`. A single UTC
cutoff is calculated when the command starts. Sessions with `last_accessed_at`
at or after the cutoff are included. This is the last accepted session activity,
not the creation time or the completion time of the latest successful turn.
`--active` can be combined with either scope and with `--since`.

The default output is a table sorted by descending `last_accessed_at`, breaking
ties by session ID and workspace path. The global table includes a `WORKSPACE`
column; the existing workspace table retains its columns. `--json` emits a
single JSON array in that order. Each entry contains `session_id`, `workspace`,
`title`, `created_at`, `last_accessed_at`, and `active`. The list never includes
messages, tool arguments, or results. An empty selection produces an empty
array or a table header.

`horizon sessions read --id ID` keeps its current-workspace restriction.
`horizon sessions read --all --id ID` resolves that ID across the chosen home,
then prints the existing history format and honors `--turns`. The original
workspace directory does not need to exist if its session file remains. A
missing ID, duplicate ID in multiple workspaces, malformed session, or registry
failure is an error. List and read do not create a turn or update session access
times. Registry recovery follows the existing store behavior when its index is
missing or damaged.

The global read mode can reveal private conversations and tool results from any
workspace in that home to the local user. Timer prompts should use `--json` for
selection, skip active sessions and their own workspace, cap the number of
histories read, and avoid copying secrets or raw tool results into memory or
skills.

Timer sessions carry optional `timer: {timer_id, task_id}` provenance, persisted
in the initial session write by `sessions create`. Both scheduled and manual
timer runs use it; forks retain it. The additive field keeps format version 1
and is classification metadata, not authorization.

List excludes marked timer sessions by default, including failed and active
ones. `--all` only changes workspace scope. Add `--include-timers` to include
them, together with any `--since`, `--active`, `--workspace` or JSON selection.
JSON entries then expose the optional `timer` object; the text table adds a
`SOURCE` column (`timer:ID`, or `-` for an unmarked session).

Direct `sessions read --all --id ID` still reads a hidden timer session. Nothing
is automatically deleted, and latest-session selection for `resume` is unchanged.
Old sessions without provenance remain visible: there is no title/workspace
heuristic migration, and missing provenance does not prove a human source.
