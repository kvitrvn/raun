#!/usr/bin/env bash
# Regenerates the committed web files: the Go code of the templates
# (ui/*_templ.go) and the stylesheet (assets/app.css). A regular build needs
# neither tool; run this after changing a template or ui/styles.css, then
# commit the result. See docs/web.md.
#
# Needs Go and curl, and the network the first time: the Tailwind binary is
# downloaded into ${WEB_TOOLS_DIR:-${TMPDIR:-/tmp}/raun-web-tools} and checked
# against its SHA-256 on every run. templ is the tool pinned in go.mod.
set -euo pipefail

cd "$(dirname "$0")/.."

# Keep in step with docs/web.md; the tests of internal/adapter/driving/web/ui
# read this line.
tailwind_version=v4.3.3

web=internal/adapter/driving/web

case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) platform=linux-x64; digest=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
    Linux-aarch64 | Linux-arm64) platform=linux-arm64; digest=55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
    Darwin-x86_64) platform=macos-x64; digest=7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1 ;;
    Darwin-arm64) platform=macos-arm64; digest=cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d ;;
    *) echo "generate-web: no pinned Tailwind binary for $(uname -s) $(uname -m); use Linux (glibc) or macOS" >&2; exit 1 ;;
esac

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum | cut -d ' ' -f 1
    else
        shasum -a 256 | cut -d ' ' -f 1
    fi
}

tools="${WEB_TOOLS_DIR:-${TMPDIR:-/tmp}/raun-web-tools}"
mkdir -p "$tools"
binary="$tools/tailwindcss-$tailwind_version-$platform"
if [[ ! -f "$binary" ]]; then
    download="$(mktemp "$tools/download.XXXXXX")"
    trap 'rm -f "$download"' EXIT
    curl --fail --location --silent --show-error --proto '=https' \
        "https://github.com/tailwindlabs/tailwindcss/releases/download/$tailwind_version/tailwindcss-$platform" \
        -o "$download"
    mv "$download" "$binary"
fi
if [[ "$(sha256 <"$binary")" != "$digest" ]]; then
    echo "generate-web: $binary does not match the pinned SHA-256; delete it and run again" >&2
    exit 1
fi
chmod +x "$binary"

# The templates first: generated files no template produces any more are
# removed, so the set always matches.
find "$web/ui" -maxdepth 1 -name '*_templ.go' -delete
go tool templ generate -path "$web/ui" >/dev/null

# The stylesheet records a hash of what produced it, which the tests check
# without running Tailwind: the Tailwind version, then the SHA-256 and the
# path of each input, in byte order of the paths.
inputs() {
    printf 'tailwindcss %s\n' "$tailwind_version"
    (
        cd "$web"
        for f in $(printf '%s\n' ui/*.templ ui/styles.css | LC_ALL=C sort); do
            printf '%s %s\n' "$(sha256 <"$f")" "$f"
        done
    )
}
stamp="$(inputs | sha256)"

css="$(mktemp)"
trap 'rm -f "$css"' EXIT
(cd "$web/ui" && "$binary" --input styles.css --output "$css" --minify >/dev/null 2>&1)
{
    printf '/* raun-inputs: %s */\n' "$stamp"
    cat "$css"
} >"$web/assets/app.css"

echo "generate-web: templates and assets/app.css are up to date"
