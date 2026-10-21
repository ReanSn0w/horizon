# Official plugin manager

`horizon plugins list` loads descriptions and dependencies from the GitHub release
matching the managed Horizon version. If GitHub is unavailable, it explicitly
labels the saved catalog for that same release. It reports installed,
missing/modified, unmanaged and available components without executing them.

```sh
horizon plugins install telegram
horizon init
horizon plugins remove telegram
horizon plugins remove decision
```

Install downloads the exact release archive, verifies SHA-256 and installs the
plugin's transitive dependencies. Telegram requires decision even if the current
Telegram configuration does not use conversation mode. Repeating an installation
of healthy files does not rewrite them. No external catalogs are supported.

Remove refuses when an installed plugin depends on the target, when it is still
in `agent_plugins`, or when it is a required base component (`plugins`, `update`).
Dependencies are not removed automatically. Configuration and plugin data remain.
Unknown files are not adopted or deleted. Use `install.sh --adopt` explicitly to
migrate binaries installed manually from this repository.

Global `--home` precedes `plugins`. A config.yaml file must exist, but provider
credentials do not have to work. Installing a plugin does not enable its agent
extension; run `horizon init`, configure it and edit `agent_plugins` explicitly.

Exit codes: 0 success/help, 1 operation failure, 2 invalid arguments, 130 cancelled.
Metadata and help work offline without initializing home. Dev or mismatched
installations require bootstrap/recovery; the manager does not guess a release.
