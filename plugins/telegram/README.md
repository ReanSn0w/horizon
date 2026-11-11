# Telegram delivery extension

The delivery plugin owns the bot credential and owner ID in `plugins.telegram`.
The input adapter shares these settings. Running the old foreground Telegram
bridge with `horizon telegram start` or `service` is no longer supported by this
binary. Stop the old service before switching to `horizon daemon start`.

The local `list` and `read` commands are intended for chat and operation
diagnostics. The old `send --message` queued an instruction for model-generated
delivery. That invocation is retired because it cannot express explicit send
semantics.
