#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [ -n "${1:-}" ]; then
  version=$1
else
  version=$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
  version=${version:-unknown}
fi
output_directory=${2:-"$repo_root/dist"}
base="df-hud-$version-linux-amd64"
stage="$output_directory/$base"
archive="$output_directory/$base.tar.gz"

rm -rf -- "$stage"
rm -f -- "$archive"
mkdir -p -- "$stage"

cd -- "$repo_root"
cargo build --locked --release --target x86_64-unknown-linux-gnu
cp -- target/x86_64-unknown-linux-gnu/release/df-hud "$stage/df-hud"

cp -- df-hud.example.toml LICENSE "$stage/"
cp -- contrib/df-hud.service "$stage/"
tar -C "$output_directory" -czf "$archive" "$(basename -- "$stage")"

printf 'Built %s\n' "$stage/df-hud"
printf 'Packaged %s\n' "$archive"
