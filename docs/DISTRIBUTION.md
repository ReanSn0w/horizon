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
