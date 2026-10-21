#!/bin/sh
set -eu
# No tag is created for a rehearsal. These artifacts are never published as latest.
HORIZON_RELEASE_TAG=${1:-v0.0.0-dev.g$(git rev-parse --short HEAD)}
HORIZON_RELEASE_VERSION=${HORIZON_RELEASE_TAG#v}
HORIZON_RELEASE_COMMIT=$(git rev-parse HEAD)
export HORIZON_RELEASE_TAG HORIZON_RELEASE_VERSION HORIZON_RELEASE_COMMIT
case $(goreleaser --version) in *2.18.3*) ;; *) printf '%s\n' 'GoReleaser 2.18.3 is required' >&2; exit 1;; esac
go run ./cmd/release-manifest --tag "$HORIZON_RELEASE_TAG" --commit "$HORIZON_RELEASE_COMMIT"
goreleaser check
goreleaser release --snapshot --clean --skip=publish
go run ./cmd/release-manifest --verify-dist dist
