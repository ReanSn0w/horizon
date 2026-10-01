package main

const gatewayHelp = `Connect a Telegram bot to per-chat Horizon workspaces and fixed sessions.
Only text messages are supported. Private chats accept the owner only.

Setup:
  make install-plugins
  horizon init
  horizon gateway init
  Edit <home>/config.yaml, then run horizon gateway start.

New groups inherit group_defaults. owner_only filters the author independently
of response_mode: false + mention allows any participant to address @bot_username.
conversation evaluates other eligible messages through the decision plugin (Jev).
Configure Horizon's provider, model and shared decision section before startup.
Group rules reload for new jobs; token, owner and workspace changes need a restart.
Disable Telegram Privacy Mode or make the bot an administrator to receive ordinary
group messages. An existing webhook must be removed explicitly before polling.

Examples:
  horizon gateway list
  horizon gateway list --json
  horizon gateway send --chat -1001234567890 -m 'Summarize the discussion' --wait
  horizon gateway service install --manager launchd
  horizon gateway service install --manager systemd
  horizon gateway service start

list shows CHAT ID beside NAME. send generates a new message from chat history,
queues it in the running gateway, and returns a request ID. It never sends -m
literally. --wait waits for delivery; interrupting it leaves the accepted job running.
Services are user LaunchAgents on macOS or systemd --user units on Linux.
Unknown external outcomes are retained and never blindly retried.
`

// Write separately: go-flags' paragraph wrapping removes YAML indentation.
const gatewayConfigHelp = `
Example config.yaml section (gateway init adds all defaults):
plugins:
  gateway:
    telegram:
      bot_token: "YOUR_BOTFATHER_TOKEN"
      owner_user_id: 123456789
    workspace_dir: /absolute/path/to/workspaces/telegram
    private_access: write
    group_access: read
    group_defaults:
      owner_only: true
      response_mode: mention
    conversation:
      history_messages: 20
      reply_threshold: 0.7
    max_parallel_chats: 2
    groups:
      "-1001234567890":
        owner_only: false
        response_mode: mention
`
