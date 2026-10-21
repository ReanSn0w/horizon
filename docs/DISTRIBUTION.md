# Official release distribution

Horizon uses one release tag for the executable and all official plugins in
`ReanSn0w/horizon`. There are no external catalogs or independently versioned
plugins. `plugins/catalog.yaml` owns descriptions and dependencies.

A release contains `release.json`, `checksums.txt` (SHA-256), and
`horizon_<version>_<os>_<arch>.tar.gz` for linux/darwin and amd64/arm64. Each
archive is flat: `horizon`, all `horizon-<plugin>` binaries, and `release.json`.
The generated manifest excludes source paths. Unknown manifest versions,
missing required components, cyclic dependencies, unexpected archive members,
links and checksum mismatches are rejected. Checksums detect corrupt downloads;
they rely on the trusted GitHub HTTPS release and are not independent signatures.

Plugin installation reads the manifest belonging to the installed tag, never
master or latest. Updates use the target tag's manifest and its dependencies.
`latest` means a stable, published GitHub release. An explicit tag can select a
prerelease. A dev build has no implicit release identity.

Managed installation state is stored in `<home>/.distribution/state.json`, apart
from config.yaml. It records the schema, manifest, platform, absolute executable
and home paths, managed plugin IDs and installed file digests. One managed
installation binds one executable path to one home. Use different binary
directories for independent managed homes. Updating one home does not coordinate
plugins in other homes that happen to invoke the same executable.

Missing state, mismatched running version or an unrecognized installation
requires explicit bootstrap/recovery with install.sh; metadata from older plugin
binaries is not used to guess a release. API keys and working model provider
settings are not required for distribution operations.

Mutations hold permanent OS locks for the home and executable path. The executable
has an adjacent `.installation.json` owner marker to reject accidental use by a
second managed home. Prepared replacements and backups live beside each target;
`<home>/.distribution/transaction.json` is persisted before any replacement.
Recovery copies backups without consuming them, so another interruption remains
recoverable. A successful durable journal removal commits the operation.

This is recoverable replacement, not an atomic switch for concurrent readers.
Stop long-running services (including Telegram) and update between agent turns.
Already running processes continue using old code. Re-run the installer to recover
if Horizon or the updater cannot start. Configuration and data are not rolled back
or migrated by the distribution mechanism. A pre-journal crash can leave harmless
hidden staging files; never delete a live transaction's backup files manually.

## Build and validation

The GitHub Actions workflow runs normal tests, vet and race tests on Linux and
macOS. It builds all four archives and runs each on a matching native runner.
Only a push of a version tag can publish; pull requests, branch pushes and
workflow_dispatch produce trial artifacts. `scripts/build-release.sh` requires
GoReleaser 2.18.3 and accepts an optional version without creating a Git tag.
The generated `.release/` and `dist/` directories are ignored by Git.

```sh
sh scripts/build-release.sh v0.1.0-rc.1
go run ./cmd/release-smoke --dist dist
```

`cmd/release-manifest` verifies the complete archive set before producing the
final checksums, including release.json and install.sh. Publication uploads into
a draft, verifies the uploaded asset names and sizes, then publishes. It refuses
to overwrite an already published release. A failed upload leaves a draft.

Subprocess E2E tests build fixtures with the explicit `horizon_distribution_test`
build tag. Only those binaries can route the fixed GitHub URLs to the local test
HTTP server. Production builds have no custom repository or endpoint option.

Initial verification on 2026-10-21: [trial workflow at 10f754d](https://github.com/ReanSn0w/horizon/actions/runs/39650338329)
passed checks on Linux/macOS, packaged all four archives and passed native smoke
tests for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64. Publication
was correctly skipped for the branch push. Local macOS arm64 and Linux arm64
(Docker) smoke and self-replacement checks also passed. Creating the first tag
and exercising actual GitHub Release publication remain separate release actions.
