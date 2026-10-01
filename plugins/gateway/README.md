# Telegram gateway

`horizon gateway` connects one Telegram bot to one Horizon home. It receives
text messages by long polling, creates a workspace and a fixed Horizon session
for each chat, and sends only successful final responses. The plugin follows
[Horizon's plugin protocol](../../docs/PLUGINS.md).

## Installation and configuration

```sh
make install-all
horizon init
horizon gateway init
# Edit ~/.horizon/config.yaml, then:
horizon gateway start
```

Use `make install-gateway` to install only this plugin. `decision` is needed
for conversation mode. Horizon's normal provider, model and shared `decision`
settings must also be configured; the default read/write access modes require
Jev for shell command evaluation. `gateway init` adds missing fields without
requiring API keys and preserves existing values, comments and file permissions.
Gateway settings are written as block YAML. Running `gateway init` again also
expands an existing inline gateway section into readable YAML without changing
its configured values. Empty `groups: {}` remains valid until a group is added.

An alternative home must be used consistently:

```sh
make install-plugins HORIZON_HOME=/absolute/path/to/home
horizon --home /absolute/path/to/home init
horizon --home /absolute/path/to/home gateway init
horizon --home /absolute/path/to/home gateway start
```

The added section is:

```yaml
plugins:
  gateway:
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
      history_messages: 20
      reply_threshold: 0.7
    max_parallel_chats: 2
    groups: {}
```

Create the bot with [@BotFather](https://t.me/BotFather), and set the owner's
numeric Telegram user ID, not a username or group ID. Keep `config.yaml` private
(`0600`). Tokens are read from this file and are not put in command arguments,
service definitions or gateway state. Participant text and generated replies
are stored locally in the gateway history and Horizon sessions.

`workspace_dir` defaults to `<home>/workspaces/telegram`; relative paths are
resolved against home. Access accepts `read`, `write` or `full`. Parallelism is
1–32 chats; history is 1–100 messages and the reply threshold is 0–1. One chat's
jobs always run in order. Each job has a ten-minute limit.

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
Jev uses the shared top-level `decision` provider and model, not separate gateway
credentials. Full decision requests are bounded to 32 KiB.

To observe ordinary group messages, disable Privacy Mode through BotFather or
make the bot an administrator; see [Telegram's bot FAQ](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get).
The gateway knows only updates it receives and sends it confirms. It does not
import older Telegram history. An existing webhook prevents startup; remove it
explicitly before choosing polling. A second polling process causes a conflict.

Group rules, access, conversation limits and parallelism are reloaded for new
jobs. Running jobs keep their snapshot. Invalid configuration pauses new jobs.
Changing the token, owner or workspace directory requires a restart. A home is
bound to one bot ID; use another home for a different bot. If inherited Horizon
access conflicts with configured access, startup fails explicitly.

## Chats and generated messages

```sh
horizon gateway list
horizon gateway list --json
horizon gateway send --chat -1001234567890
horizon gateway send --chat -1001234567890 -m 'Summarize the recent discussion' --wait
horizon gateway send --chat -1001234567890 --thread 42 --json
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

`send` requires a running gateway and a known available chat. It queues a new
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
horizon gateway service install --manager launchd
horizon gateway service start
horizon gateway service status
horizon gateway service restart
horizon gateway service stop
horizon gateway service uninstall
```

The LaunchAgent is installed in `~/Library/LaunchAgents`. It runs in the logged-in
user's GUI domain, rather than as a system daemon; see [Apple's launchd documentation](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).
Diagnostics go to `<home>/gateway/service.log`, rotated at 1 MiB with one backup.
`stop` disables autostart until `start`, `restart` or `install` enables it again.

On Linux use the same commands with `install --manager systemd`. The unit is
installed under `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`)
and controlled with `systemctl --user`. Logs are in the user journal:

```sh
journalctl --user -u io.horizon.gateway.<home-hash>.service
```

`status` prints the exact unit name, installation, enabled/running state and
whether the gateway's process lock is held. A missing user manager is an error.
Running outside login sessions may require administrator-configured lingering;
see [loginctl](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html).
The plugin does not change lingering policy. Unit paths follow
[systemd.unit](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html).

Service names derive from the normalized home path. Definitions contain absolute
Horizon and home paths and the installation-time PATH, but no bot/API tokens.
Reinstall after moving Horizon or changing the tool PATH. `install` enables future
autostart; `start` runs now. `uninstall` stops and removes only this gateway's
service definition, preserving configuration, state and sessions. Do not run a
foreground instance alongside a service: a home-wide lock prevents duplicates.

## Persistence and failure handling

State lives in `<home>/gateway/telegram/<bot_id>/state.yaml`, protected by a
permanent lock and atomic writes. Workspaces use numeric bot/chat IDs and retain
their binding through a group-to-supergroup migration. The gateway keeps up to
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
4. Restart the gateway and verify the same workspace/session persists without
   duplicate replies; verify rename and removal update `list`.
5. Install/start/status/stop/uninstall the user service on the target OS, then
   confirm its definition is removed and home data remains.

The real-bot smoke test requires a supplied test bot and has not been executed
as part of automated development checks.
