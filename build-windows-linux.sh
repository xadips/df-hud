#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
version=${1:-0.1.0-windows}
output_directory=${2:-"$repo_root/dist"}
mingw_root=${MINGW_ROOT:-/usr/x86_64-w64-mingw32/sys-root/mingw}
mingw_bin="$mingw_root/bin"
cc=${CC:-x86_64-w64-mingw32-gcc}
objdump=${OBJDUMP:-x86_64-w64-mingw32-objdump}
pkg_config=${PKG_CONFIG:-x86_64-w64-mingw32-pkg-config}
windres=${WINDRES:-x86_64-w64-mingw32-windres}
stage="$output_directory/df-hud-windows-amd64"
archive="$output_directory/df-hud-windows-amd64.zip"
executable="$stage/df-hud.exe"
resource_object="$repo_root/cmd/df-hud/df-hud_windows_amd64.syso"

required_commands=(
    "$cc"
    go
    "$objdump"
    "$pkg_config"
    "$windres"
    zip
)
for command_name in "${required_commands[@]}"; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        printf 'required build tool not found: %s\n' "$command_name" >&2
        exit 1
    fi
done

if [[ ! -d "$mingw_bin" ]]; then
    printf 'MinGW runtime directory not found: %s\n' "$mingw_bin" >&2
    exit 1
fi
if ! "$pkg_config" --exists gtk4 gobject-introspection-1.0; then
    printf 'pkg-config cannot resolve gtk4 and gobject-introspection-1.0\n' >&2
    exit 1
fi
if [[ -e "$resource_object" ]]; then
    printf 'refusing to overwrite generated resource: %s\n' "$resource_object" >&2
    exit 1
fi

rm -rf -- "$stage"
rm -f -- "$archive"
mkdir -p -- "$stage"

cleanup() {
    rm -f -- "$resource_object"
}
trap cleanup EXIT

cd -- "$repo_root"
"$windres" df-hud.rc -O coff -o "$resource_object"

CGO_ENABLED=1 \
GOOS=windows \
GOARCH=amd64 \
CC="$cc" \
PKG_CONFIG="$pkg_config" \
go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags "-H=windowsgui -X main.version=$version" \
    -o "$executable" ./cmd/df-hud

cp -- df-hud.exe.manifest df-hud.example.toml LICENSE "$stage/"

runtime_trees=(
    etc/gtk-4.0
    lib/gdk-pixbuf-2.0
    lib/gio/modules
    lib/gtk-4.0
    share/glib-2.0/schemas
    share/gtk-4.0
    share/icons/Adwaita
    share/icons/hicolor
)
for tree in "${runtime_trees[@]}"; do
    source_path="$mingw_root/$tree"
    if [[ ! -e "$source_path" ]]; then
        continue
    fi
    destination="$stage/$tree"
    mkdir -p -- "$(dirname -- "$destination")"
    cp -a -- "$source_path" "$destination"
done

is_windows_system_dll() {
    case "${1,,}" in
        api-ms-win-*|ext-ms-win-*|\
        advapi32.dll|bcrypt.dll|bcryptprimitives.dll|cfgmgr32.dll|\
        comctl32.dll|comdlg32.dll|\
        crypt32.dll|d2d1.dll|d3d9.dll|d3d11.dll|d3d12.dll|dcomp.dll|\
        dnsapi.dll|dwrite.dll|dwmapi.dll|dxgi.dll|gdi32.dll|gdiplus.dll|\
        hid.dll|imm32.dll|\
        iphlpapi.dll|kernel32.dll|msimg32.dll|msvcrt.dll|ntdll.dll|ole32.dll|\
        oleaut32.dll|opengl32.dll|propsys.dll|rpcrt4.dll|secur32.dll|\
        setupapi.dll|shcore.dll|shell32.dll|shlwapi.dll|user32.dll|usp10.dll|\
        userenv.dll|uxtheme.dll|version.dll|winhttp.dll|winmm.dll|winspool.drv|\
        wintrust.dll|ws2_32.dll)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

resolve_runtime_dll() {
    local wanted=${1,,}
    local candidate name
    shopt -s nullglob
    for candidate in "$mingw_bin"/*.dll; do
        name=${candidate##*/}
        if [[ ${name,,} == "$wanted" ]]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

shopt -s globstar nullglob
queue=("$executable" "$stage"/**/*.dll)
declare -A inspected=()
declare -A unresolved=()
index=0
while (( index < ${#queue[@]} )); do
    binary=${queue[$index]}
    ((index += 1))
    binary_key=${binary,,}
    if [[ -n ${inspected[$binary_key]:-} ]]; then
        continue
    fi
    inspected[$binary_key]=1

    while IFS= read -r dependency; do
        [[ -n "$dependency" ]] || continue
        target="$stage/$dependency"
        if [[ -e "$target" ]]; then
            queue+=("$target")
            continue
        fi
        if source_dll=$(resolve_runtime_dll "$dependency"); then
            target="$stage/${source_dll##*/}"
            if [[ ! -e "$target" ]]; then
                cp -- "$source_dll" "$target"
            fi
            queue+=("$target")
            continue
        fi
        if ! is_windows_system_dll "$dependency"; then
            unresolved["${dependency,,}"]="$dependency (from ${binary##*/})"
        fi
    done < <(
        "$objdump" -p "$binary" |
            awk '/DLL Name:/ { print $3 }'
    )
done

if (( ${#unresolved[@]} > 0 )); then
    printf 'unresolved Windows DLL dependencies:\n' >&2
    for dependency in "${unresolved[@]}"; do
        printf '  %s\n' "$dependency" >&2
    done
    exit 1
fi

(
    cd -- "$stage"
    zip -qr "$archive" .
)

printf 'Built %s\n' "$executable"
printf 'Packaged %s\n' "$archive"
