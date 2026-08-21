#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
version=${1:-0.1.0-linux}
output_directory=${2:-"$repo_root/dist"}
base="df-hud-$version-linux-amd64"
stage="$output_directory/$base"
archive="$output_directory/$base.tar.gz"

rm -rf -- "$stage"
rm -f -- "$archive"
mkdir -p -- "$stage"

cd -- "$repo_root"
go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags "-X main.version=$version" \
    -o "$stage/df-hud" ./cmd/df-hud

cp -- df-hud.example.toml LICENSE "$stage/"
cp -- contrib/df-hud.service "$stage/"
tar -C "$output_directory" -czf "$archive" "$(basename -- "$stage")"

printf 'Built %s\n' "$stage/df-hud"
printf 'Packaged %s\n' "$archive"
