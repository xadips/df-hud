#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
version=${1:-0.1.0-linux}
output_directory=${2:-"$repo_root/dist"}
stage="$output_directory/df-hud-linux-amd64"
archive="$output_directory/df-hud-linux-amd64.tar.gz"

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
