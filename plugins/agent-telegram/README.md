# Telegram input adapter

The daemon starts this adapter only when `telegram` is explicitly listed in
`plugins.daemon.adapters`. Configure its workspace, access, groups and input
limits in `plugins.agent-telegram`. The adapter uses the credentials in
`plugins.telegram` and has no separate token.
