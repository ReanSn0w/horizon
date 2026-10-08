# Telegram integration

`horizon telegram` connects one Telegram bot to one Horizon home. It receives
text messages by long polling, creates a workspace and a fixed Horizon session
for each chat, and sends only successful final responses. The plugin follows
[Horizon's plugin protocol](../../docs/PLUGINS.md).

## Installation and configuration

```sh
make install-all
horizon init
# Edit ~/.horizon/config.yaml, then:
horizon telegram start
```

Use `make install-telegram` to install only this plugin. `decision` is needed
for conversation mode. Horizon's normal provider, model and shared `decision`
settings must also be configured; the default read/write access modes require
Jev for shell command evaluation. `horizon init` adds missing Telegram fields
without requiring API keys and preserves existing values, comments and file
permissions. Run it again after installing the plugin into an existing home.
Empty `groups: {}` remains valid until a group is added.

### Upgrade from gateway

The plugin command and executable are now `telegram` and `horizon-telegram`.
Stop the old foreground process or run `horizon gateway service stop` before
switching. Install the new binary with `make install-telegram`, then run
`horizon init`: it renames `plugins.gateway` to `plugins.telegram`
under the configuration lock, preserving values and comments. If both sections
exist, init refuses to choose one; keep the intended section before retrying.

State paths under `<home>/gateway/`, workspace markers `.horizon-gateway.json`,
and service identifiers `io.horizon.gateway.<home-hash>` are retained for
compatibility. Existing chats, sessions and queued jobs remain available.
For an installed user service, run `horizon telegram service install` again
to update its command arguments, then `horizon telegram service restart`.
The old installed `horizon-gateway` executable is not removed automatically;
remove it after the switch. Do not run both plugin versions together.

An alternative home must be used consistently:

```sh
make install-plugins HORIZON_HOME=/absolute/path/to/home
horizon --home /absolute/path/to/home init
horizon --home /absolute/path/to/home telegram start
```

The added section is:

```yaml
plugins:
  telegram:
    telegram:
      bot_token: "YOUR_BOTFATHER_TOKEN"
      owner_user_id: 123456789
    workspace_dir: /absolute/path/to/home/workspaces/telegram
    private_access: write
    group_access: read
    group_defaults:
      owner_only: true
      response_mode: mention
    conversation:
      bot_names: []
      history_messages: 20
      reply_threshold: 0.7
      idle_compact_after: 12h
    max_parallel_chats: 2
    groups: {}
```

Create the bot with [@BotFather](https://t.me/BotFather), and set the owner's
numeric Telegram user ID, not a username or group ID. Keep `config.yaml` private
(`0600`). Tokens are read from this file and are not put in command arguments,
service definitions or telegram state. Participant text and generated replies
are stored locally in the telegram history and Horizon sessions.

`workspace_dir` defaults to `<home>/workspaces/telegram`; relative paths are
resolved against home. Access accepts `read`, `write` or `full`. Parallelism is
1–32 chats; history is 1–100 messages and the reply threshold is 0–1. One chat's
jobs always run in order. Available chats are scheduled in round-robin order,
so a busy group cannot indefinitely precede private chats. Each job and initial
workspace preparation has a ten-minute limit.

### Idle session compaction

After 12 hours without an incoming message, the running bridge checks whether
the chat's Horizon session has a new completed turn and a working checkpoint of
at least 128 KiB. If so, it calls `/responses/compact` in the background. This
applies to private and group chats. Set `idle_compact_after: 6h` for an earlier
check or `idle_compact_after: 0` to disable it; any other positive duration must
be at least one hour. Re-run `horizon init` after upgrading to add the missing
setting to an existing config without changing values or comments.

Only one background compaction runs at a time. Normal chat jobs take priority;
a new message cancels maintenance for that chat before its next reply. A failed
attempt is retried no sooner than one hour later. The bridge skips sessions
whose latest completed turn is already compacted. The operation creates no
Telegram message or agent turn, but it adds a provider request that may cost
tokens. Checkpoint bytes are only an estimate of possible savings; they do not
predict billed tokens or a faster first reply.

Telegram's separate `conversation` field still carries recent chat messages
when the next job runs. Session compaction does not shrink that field. For
timing and token measurements, see [the diagnostic guide](../../docs/TELEGRAM_LATENCY.md).

Private messages from anyone except the owner are discarded before registration
or model use. Group settings are added to `groups` when a group is first observed.
Change individual groups independently:

```yaml
    groups:
      "-1001234567890":
        owner_only: false
        response_mode: mention
      "-1009876543210":
        owner_only: true
        response_mode: conversation
```

`owner_only` filters who can trigger a reply. With `false` and `mention`, any
participant can address the bot with its exact `@username`. With `true`, only
the owner can trigger replies; other participants' text can still provide
context. Replies to a bot message without `@` do not bypass these rules.

In `conversation` mode an eligible author's mention triggers a reply directly.
Other text is evaluated through `horizon decision -` with recent chat messages
and known bot replies. A valid `should_reply.noul` at or above `reply_threshold`
allows generation. An error, timeout or malformed result prevents that reply.
Jev uses the shared top-level `decision` provider and model, not separate telegram
credentials. Full decision requests are bounded to 32 KiB.

Set `plugins.telegram.conversation.bot_names` to the names and nicknames people
use for the bot, for example:

```yaml
    conversation:
      bot_names: ["Курису", "Кристина", "Kurisu"]
      history_messages: 20
      reply_threshold: 0.7
```

The list applies to all conversation-mode groups and is passed to Jev as data.
The evaluator is instructed to treat direct address as strong evidence that a
reply is expected, considering case and natural name forms, while distinguishing
it from quotes, incidental mentions or discussion of someone with the same name.
Names do not bypass `owner_only` or `reply_threshold`, and do not change `mention`
mode. This is contextual model evaluation, not a guaranteed keyword trigger.
The default is an empty list; existing configurations need no migration.
Up to 32 non-blank names of at most 128 characters each are accepted. Changes
apply to new jobs without restarting the plugin.

To observe ordinary group messages, disable Privacy Mode through BotFather or
make the bot an administrator; see [Telegram's bot FAQ](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get).
The telegram knows only updates it receives and sends it confirms. It does not
import older Telegram history. An existing webhook prevents startup; remove it
explicitly before choosing polling. A second polling process causes a conflict.

Group rules, access, conversation limits and parallelism are reloaded for new
jobs. Running jobs keep their snapshot. Invalid configuration pauses new jobs.
Changing the token, owner or workspace directory requires a restart. A home is
bound to one bot ID; use another home for a different bot. If inherited Horizon
access conflicts with configured access, startup fails explicitly.

## Chats and generated messages

```sh
horizon telegram list
horizon telegram list --json
horizon telegram send --chat -1001234567890
horizon telegram send --chat -1001234567890 -m 'Summarize the recent discussion' --wait
horizon telegram send --chat -1001234567890 --thread 42 --json
```

`list` reads local state without network or model calls. `CHAT ID` and `NAME`
are adjacent; names come from the group's title or the owner's name, then fall
back to `@username` and `Chat <id>`. Names reflect the last received metadata.
Duplicate names are allowed: use the numeric chat ID for addressing. The table
also shows availability, workspace, session, rules, last message time, queue,
unknown outcomes and latest status/error. It omits conversation text.

`--json` prints one object with `schema_version: 1` and `chats: []`. Each chat
includes `chat_id` as a decimal string, `name`, `type`, `available`, `workspace`,
`session_id`, `owner_only`, `response_mode`, `last_message_at` (RFC3339 or null),
`pending`, `unknown`, and optional latest request/status/error fields.

`send` requires a running telegram and a known available chat. It queues a new
Horizon turn in the chat's existing session. `-m` is an instruction to generate a
reply using recent history; it is not sent literally. Without `-m`, Horizon is
asked to continue the conversation. Manual sends bypass reply eligibility
checks, while retaining the chat's access mode. The command immediately returns
request ID and `queued`; `--wait` waits for delivery, and `--json` returns a
structured result. Interrupting the wait leaves the accepted job running.

Forum topics share one chat workspace and session. Incoming replies return to
the original topic; manual `send --thread ID` selects a topic. Without it the
last received text topic is used; when no topic is known, specify one explicitly.
Attachments, captions, voice, edited messages, channels and anonymous group
authors do not trigger responses in this version.

## User services

On macOS:

```sh
horizon telegram service install --manager launchd
horizon telegram service start
horizon telegram service status
horizon telegram service restart
horizon telegram service stop
horizon telegram service uninstall
```

The LaunchAgent is installed in `~/Library/LaunchAgents`. It runs in the logged-in
user's GUI domain, rather than as a system daemon; see [Apple's launchd documentation](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).
Diagnostics go to `<home>/gateway/service.log` as versioned JSONL, rotated at
1 MiB with one backup. A legacy text journal is archived before JSONL starts.
Early Horizon/plugin startup errors and fallback diagnostics go to
`<home>/gateway/launchd.log`. The Horizon host bounds this fallback to 1 MiB by
truncating the same open file; renaming a file would leave launchd writing to its
old descriptor. A failure before Horizon can execute may require launchd's own
diagnostics. New log files are private (`0600`).
`stop` disables autostart until `start`, `restart` or `install` enables it again.

On Linux use the same commands with `install --manager systemd`. The unit is
installed under `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`)
and controlled with `systemctl --user`. Logs are in the user journal:

```sh
journalctl --user -u io.horizon.gateway.<home-hash>.service
```

`status` prints the exact unit name, installation, enabled/running state and
whether the telegram's process lock is held. It also prints the installed log
destinations, warnings about legacy definitions, and the daemon health snapshot.
A missing user manager is an error.
Running outside login sessions may require administrator-configured lingering;
see [loginctl](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html).
The plugin does not change lingering policy. Unit paths follow
[systemd.unit](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html).

Service names derive from the normalized home path. Definitions contain absolute
Horizon and home paths and the installation-time PATH, but no bot/API tokens.
Reinstall after moving Horizon or changing the tool PATH. `install` enables future
autostart; `start` runs now. `uninstall` stops and removes only this telegram's
service definition, preserving configuration, state and sessions. Do not run a
foreground instance alongside a service: a home-wide lock prevents duplicates.

## Diagnostics and upgrades

After installing the new binaries, update an existing macOS service definition:

```sh
make install-all
horizon telegram service install --manager launchd
horizon telegram service restart
horizon telegram service status
```

`restart` unloads the old launchd definition and loads the saved plist before
starting the daemon. Merely replacing a binary or editing its plist does not
change the arguments and stderr destination of an already loaded job. Use the
same `--home` throughout when working with an alternative home. Foreground
`start` writes JSONL to stderr; add `--log-file /absolute/path/service.log` to
save it. An explicitly selected journal is checked before polling starts.
Write/rotation failures are reported through the fallback rather than silently
ignored; they never authorize repeating an external action.

Events include daemon lifecycle, received updates, saved offsets, queued jobs,
reply decisions, Horizon model/tool activity, delivery confirmations and errors.
Correlate `data.chat_id` and `data.message_id` with `data.request_id`, then
`data.session_id` and `data.turn_id`. `run_id` identifies one daemon run.
`reply_decision` records the reason, threshold, duration and Jev score when
available; a deliberate skip is distinct from an evaluation failure. Repeated
polling/configuration errors are suppressed until their cause changes or recovers.

Only selected metadata enters the journal: conversation text, generated answers,
shell commands, raw tool output and API credentials are excluded. Full results
remain in existing private sessions/artifacts. Bounded, credential-redacted
child stderr is accessible through local Telegram state/`telegram list`; the
service journal records the exit category instead of arbitrary provider text.
Each journal record is limited to 64 KiB; oversized records are replaced by an
explicit `diagnostic_record_oversized` event.

`<home>/gateway/health.json` is an atomic snapshot updated about once per second,
independently of the queue's state lock. `service status` shows last successful
polling, last offset advance, observed queue size, occupied slots, current jobs
and the time spent in polling/scheduler/job stages. Queue counts are the last
observed counts and may be older if the scheduler is waiting on a lock. Snapshots
older than three seconds or belonging to a stopped service are marked stale.
An idle chat without messages is not evidence of a stuck daemon.

`telegram list --json` retains existing fields and additionally exposes
`current_request_id`, `current_stage`, `current_stage_since`, and
`oldest_queue_wait_ms` when known. The current job is shown separately from
the last queued job. Old saved jobs without a queue timestamp remain readable.

Every generated reply starts a new Horizon process with a fresh skill catalog.
A skill created or enabled between turns is available in the next turn without
restarting Telegram; the running turn keeps its immutable snapshot.
`horizon_turn_started` records skill IDs/names, the selected home and the SHA-256
of the actual instructions sent to the provider, without logging their content.

The starvation regression test demonstrated that sorting chats by numeric ID
can postpone a private chat behind a group when one slot is available.
Round-robin scheduling fixes this reproduced defect. The original October 1
Mac Mini incident was not captured, so its exact cause remains unconfirmed.

## Persistence and failure handling

State lives in `<home>/gateway/telegram/<bot_id>/state.yaml`, protected by a
permanent lock and atomic writes. Workspaces use numeric bot/chat IDs and retain
their binding through a group-to-supergroup migration. The telegram keeps up to
100 recent messages and 100 completed job results per chat, plus pending and
unknown jobs. Pending queues are limited to 100 per chat and 1000 overall.
Updates are acknowledged only after they have been saved.

Successful Horizon output is saved before delivery. Messages are plain text,
with no Markdown parsing, split at Unicode boundaries. Confirmed fragments are
recorded separately. After a crash, queued/evaluating jobs can resume and saved
unsent responses can be delivered. Interrupted generation or sending is marked
`unknown` and is never blindly replayed; network failures can have external
side effects even when no confirmation arrives. `list` shows the unknown count.
A fresh explicit request creates a new job; it does not retry an old unknown job.
This is not an exactly-once delivery guarantee.

Workspace isolation is routing, not an OS sandbox. Shell commands retain the
existing Horizon access policy and user privileges. The plugin never edits
Horizon's internal session JSON directly.

## Validation and manual smoke test

Automated tests use temporary homes, fake Telegram/Responses/Decisions APIs,
real built binaries, and substituted service managers. They do not use a real
bot or provider. Native LaunchAgent/systemd lifecycle still needs validation on
the target host; systemd was not run on the development macOS host.

With a dedicated test bot and configured providers:

1. Start in a temporary home, message it from the owner and from another account;
   only the owner's private chat should appear in `list` and receive a response.
2. Add it to a test group, inspect the generated group settings and readable name.
   Test owner/nonowner mentions with both `owner_only` values.
3. Enable `conversation`, send several contextual messages and verify Jev decides
   when participation is useful. Test `send -m ... --wait` from another terminal.
4. Restart the telegram and verify the same workspace/session persists without
   duplicate replies; verify rename and removal update `list`.
5. Install/start/status/stop/uninstall the user service on the target OS, then
   confirm its definition is removed and home data remains.

The real-bot smoke test requires a supplied test bot and has not been executed
as part of automated development checks.

### Target Mac Mini smoke test

Use a dedicated test bot/home on the Mac Mini M4; these steps are manual and
require configured real providers. After updating binaries and reinstalling
the LaunchAgent:

1. Run `horizon telegram service status` and confirm the primary and fallback
   destinations are present, the snapshot is fresh and polling advances.
2. Send messages to a test conversation-mode group for which Jev returns a
   negative score. Confirm `reply_decision` records a skip with its score.
3. Send an owner private message while group jobs are pending. Confirm the
   journal links receipt, queue, Horizon completion and confirmed delivery
   without restarting the daemon. Inspect occupied slots and stage ages if late.
4. Add a valid test skill under the selected home's `skills/` between turns.
   Send a fresh private message and verify the new ID/name appears in the next
   `horizon_turn_started`; ask the agent to read the skill and inspect the tool
   result in its local session. Disable the skill and verify the following turn
   omits it, with no daemon restart.
5. Stop/start the service and verify a new run ID, preserved chat bindings and
   no replay of unknown generation/sending outcomes.

Local automated tests cover fake APIs, built binaries and substituted managers.
They do not establish real launchd, Telegram or provider behavior on the target Mac.
