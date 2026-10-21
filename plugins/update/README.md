# Release updater

```sh
horizon update
horizon update --version v0.1.0
```

The default is the latest stable GitHub release from ReanSn0w/horizon. An explicit
tag may select a prerelease. Downgrades are rejected. The updater installs the
core, itself, the plugin manager, all previously managed plugins and any new
transitive dependencies. A removed plugin in the target catalog stops the update.
An already healthy installation at the target version is not rewritten.

Finish agent turns and stop long-running services before updating. With Telegram:

```sh
horizon telegram service stop
horizon update
horizon init
horizon telegram service start
```

Use the same global `--home` throughout. Updates preserve paths, config.yaml,
instructions, sessions, memory, skills and unknown files. Services are not started
automatically. No working provider credentials are needed, although the host still
requires config.yaml before executing a normal plugin command.

Replacement uses locks, staging files, backups and a persistent transaction
journal. This provides recovery, not an atomic switch visible to concurrently
running processes. Re-run install.sh after interruption if the updater cannot
start. `--adopt` explicitly migrates existing official binaries from a source
installation; it does not follow ambiguous symlinks or adopt unrelated filenames.

The `bootstrap --bundle DIR --home DIR --bin-dir DIR [--adopt]` subcommand is an
internal installer entry point. It accepts the complete, checksum-verified bundle
already extracted by install.sh and checks its identity against its own build.
The installer also passes `--expected-tag TAG` to require the selected release.
It is not a general package source or an alternate plugin repository.

Exit codes: 0 success/help, 1 operation failure, 2 invalid arguments, 130 cancelled.
See [distribution internals](../../docs/DISTRIBUTION.md) for journal and ownership
rules. Separate managed homes require separate core binary paths.
