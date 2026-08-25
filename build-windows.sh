#!/bin/sh
set -eu

if [ -n "${1:-}" ]; then
	VERSION=$1
else
	VERSION=$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
	VERSION=${VERSION:-unknown}
fi
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OUT=${OUTPUT_DIRECTORY:-"$ROOT/dist"}
BASE="df-hud-$VERSION-windows-amd64"
STAGE="$OUT/$BASE"
ARCHIVE="$OUT/$BASE.zip"

command -v zip >/dev/null 2>&1 || {
	printf >&2 'missing zip\n'
	exit 1
}

mkdir -p "$OUT"
rm -rf "$STAGE"
mkdir -p "$STAGE"

# See build-linux.sh: strip does not remove panic-location paths.
CARGO_HOME_DIR=${CARGO_HOME:-$HOME/.cargo}
RUSTUP_HOME_DIR=${RUSTUP_HOME:-$HOME/.rustup}
RUSTFLAGS="${RUSTFLAGS:-} --remap-path-prefix=$CARGO_HOME_DIR/registry=/cargo/registry --remap-path-prefix=$RUSTUP_HOME_DIR=/rustup --remap-path-prefix=$ROOT=/build"
export RUSTFLAGS

(
	cd "$ROOT"
	cargo build --locked --release --target x86_64-pc-windows-gnu
)
cp "$ROOT/target/x86_64-pc-windows-gnu/release/df-hud.exe" "$STAGE/df-hud.exe"
cp "$ROOT/df-hud.example.toml" "$ROOT/LICENSE" "$STAGE/"
# OFL 1.1 requires the license to travel with the embedded font.
cp "$ROOT/assets/fonts/JetBrainsMono-OFL.txt" "$STAGE/"
rm -f "$ARCHIVE"
(
	cd "$STAGE"
	zip -q -r "$ARCHIVE" .
)

printf 'Built %s\n' "$STAGE/df-hud.exe"
printf 'Packaged %s\n' "$ARCHIVE"
