#!/bin/sh
set -eu

VERSION=${1:-0.1.0-windows}
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OUT=${OUTPUT_DIRECTORY:-"$ROOT/dist"}
BASE="df-hud-$VERSION-windows-amd64"
STAGE="$OUT/$BASE"
ARCHIVE="$OUT/$BASE.zip"
RESOURCE="$ROOT/cmd/df-hud/df-hud_windows_amd64.syso"
WINDRES=${WINDRES:-x86_64-w64-mingw32-windres}

command -v "$WINDRES" >/dev/null 2>&1 || {
	printf >&2 'missing %s (install binutils-mingw-w64-x86-64)\n' "$WINDRES"
	exit 1
}
command -v zip >/dev/null 2>&1 || {
	printf >&2 'missing zip\n'
	exit 1
}

mkdir -p "$OUT"
rm -rf "$STAGE"
mkdir -p "$STAGE"
trap 'rm -f "$RESOURCE"' EXIT INT TERM

"$WINDRES" "$ROOT/df-hud.rc" -O coff -o "$RESOURCE"
(
	cd "$ROOT"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags "-H=windowsgui -X main.version=$VERSION" \
		-o "$STAGE/df-hud.exe" ./cmd/df-hud
)

cp "$ROOT/df-hud.example.toml" "$ROOT/LICENSE" "$STAGE/"
rm -f "$ARCHIVE"
(
	cd "$STAGE"
	zip -q -r "$ARCHIVE" .
)

printf 'Built %s\n' "$STAGE/df-hud.exe"
printf 'Packaged %s\n' "$ARCHIVE"
