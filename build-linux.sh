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

# Keep the builder's home out of the shipped binary. `strip` drops symbols but
# not the panic-location strings, which otherwise carry an absolute path for
# every dependency. CI builds as `runner`, but this script also runs by hand.
cargo_home=${CARGO_HOME:-$HOME/.cargo}
rustup_home=${RUSTUP_HOME:-$HOME/.rustup}
export RUSTFLAGS="${RUSTFLAGS:-} --remap-path-prefix=$cargo_home/registry=/cargo/registry --remap-path-prefix=$rustup_home=/rustup --remap-path-prefix=$repo_root=/build"

cargo build --locked --release --target x86_64-unknown-linux-gnu
cp -- target/x86_64-unknown-linux-gnu/release/df-hud "$stage/df-hud"

cp -- df-hud.example.toml LICENSE "$stage/"
cp -- contrib/df-hud.service "$stage/"
tar -C "$output_directory" -czf "$archive" "$(basename -- "$stage")"

printf 'Built %s\n' "$stage/df-hud"
printf 'Packaged %s\n' "$archive"
