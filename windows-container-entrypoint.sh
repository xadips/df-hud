#!/bin/sh
set -eu

version=${1:-0.1.0-windows}
case "$version" in
    *[!A-Za-z0-9._+-]*)
        printf 'invalid version string: %s\n' "$version" >&2
        exit 1
        ;;
esac

bash /src/build-windows-linux.sh "$version"

if [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
    chown -R "$HOST_UID:$HOST_GID" /src/dist
fi
