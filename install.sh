#!/bin/sh
# Bootstrap only from the official Horizon release. No Go or provider key required.
set -eu
umask 077
export LC_ALL=C
release_tag=
install_bin=${HOME:?HOME is required}/.local/bin
install_home=${HORIZON_HOME:-$HOME/.horizon}
adopt=no
usage() {
    printf '%s\n' 'Usage: sh install.sh [--version vX.Y.Z] [--bin-dir PATH] [--home PATH] [--adopt]' \
        'Stop running Horizon services before updating. Existing data is preserved.'
}
fail() { printf 'install: %s\n' "$*" >&2; exit 1; }
while [ "$#" -gt 0 ]; do
    case "$1" in
        --version|--bin-dir|--home)
            [ "$#" -ge 2 ] && [ -n "$2" ] || fail "missing value for $1"
            case "$1" in --version) release_tag=$2;; --bin-dir) install_bin=$2;; --home) install_home=$2;; esac
            shift 2 ;;
        --adopt) adopt=yes; shift ;;
        --help|-h) usage; exit 0 ;;
        *) usage >&2; fail "unknown option $1" ;;
    esac
done
for utility in uname curl tar gzip head wc awk grep mktemp mkdir rm chmod; do
    command -v "$utility" >/dev/null 2>&1 || fail "required utility is missing: $utility"
done
if command -v sha256sum >/dev/null 2>&1; then checksum=sha256sum
elif command -v shasum >/dev/null 2>&1; then checksum=shasum
else fail 'sha256sum or shasum is required'; fi
case $(uname -s) in Linux) os=linux;; Darwin) os=darwin;; *) fail 'supported systems: Linux and macOS';; esac
case $(uname -m) in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) fail 'supported architectures: amd64 and arm64';; esac
repository=https://github.com/ReanSn0w/horizon
if [ -z "$release_tag" ]; then
    final_url=$(curl -fLsS --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 60 -o /dev/null -w '%{url_effective}' "$repository/releases/latest") || fail 'cannot resolve latest stable release'
    case "$final_url" in "$repository/releases/tag/"*) release_tag=${final_url##*/};; *) fail 'unexpected latest release URL';; esac
fi
printf '%s\n' "$release_tag" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || fail 'invalid release tag'
archive=horizon_${release_tag#v}_${os}_${arch}.tar.gz
staging=$(mktemp -d "${TMPDIR:-/tmp}/horizon-install.XXXXXXXX") || fail 'cannot create temporary directory'
trap 'rm -rf "$staging"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
download() {
    curl -fLsS --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 300 --retry 2 --max-filesize "$3" -o "$2" "$1" || fail "download failed: $1"
}
download "$repository/releases/download/$release_tag/checksums.txt" "$staging/checksums.txt" 65536
download "$repository/releases/download/$release_tag/$archive" "$staging/$archive" 268435456
expected=$(awk -v name="$archive" '{file=$2; sub(/^\*/,"",file); if(file==name){count++;hash=$1}} END{if(count!=1)exit 1; print hash}' "$staging/checksums.txt") || fail 'archive checksum missing or duplicated'
printf '%s\n' "$expected" | grep -Eq '^[0-9a-f]{64}$' || fail 'invalid checksum'
if [ "$checksum" = sha256sum ]; then actual=$(sha256sum "$staging/$archive"); else actual=$(shasum -a 256 "$staging/$archive"); fi
actual=${actual%% *}
[ "$actual" = "$expected" ] || fail 'archive checksum mismatch'
# Bound decompressed data before extraction; then validate the complete gzip stream.
expanded_size=$(gzip -dc "$staging/$archive" | head -c 536870913 | wc -c)
[ "$expanded_size" -le 536870912 ] || fail 'expanded archive exceeds 512 MiB'
gzip -t "$staging/$archive" || fail 'invalid gzip archive'
tar -tzf "$staging/$archive" > "$staging/members"
awk 'BEGIN{ok=1} !/^(horizon(-[a-z][a-z0-9-]*)?|release\.json)$/{ok=0} {if(seen[$0]++)ok=0} END{if(!seen["horizon"]||!seen["horizon-update"]||!seen["horizon-plugins"]||!seen["release.json"]||NR>128)ok=0; exit !ok}' "$staging/members" || fail 'unexpected or duplicate archive paths'
tar -tvzf "$staging/$archive" > "$staging/types"
awk 'substr($0,1,1)!="-"{bad=1} END{exit bad}' "$staging/types" || fail 'archive contains links or special files'
mkdir "$staging/bundle"
tar -xzf "$staging/$archive" -C "$staging/bundle"
chmod 700 "$staging/bundle/horizon-update"
set -- bootstrap --bundle "$staging/bundle" --home "$install_home" --bin-dir "$install_bin"
if [ "$adopt" = yes ]; then set -- "$@" --adopt; fi
"$staging/bundle/horizon-update" "$@"
case ":${PATH:-}:" in *":$install_bin:"*) ;; *) printf 'Add this binary directory to PATH: %s\n' "$install_bin";; esac
