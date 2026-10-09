#!/bin/sh
# Regenerate committed web resources. No tooling is needed for a normal Go build.
set -eu
cd "$(dirname "$0")/.."
templ_version=v0.3.1070
tailwind_version=v4.3.3
tools_dir=${RAUN_WEB_TOOLS:-${TMPDIR:-/tmp}/raun-web-tools}
mkdir -p "$tools_dir"
tools_dir=$(cd "$tools_dir" && pwd)
case "$(uname -s)-$(uname -m)" in
 Linux-x86_64) platform=linux-x64; digest=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
 Linux-aarch64|Linux-arm64) platform=linux-arm64; digest=55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
 Darwin-x86_64) platform=macos-x64; digest=7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1 ;;
 Darwin-arm64) platform=macos-arm64; digest=cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d ;;
 *) echo "Unsupported platform for asset generation (use Linux glibc or macOS)." >&2; exit 1 ;;
esac
binary="$tools_dir/tailwindcss-$tailwind_version-$platform"
if [ ! -f "$binary" ]; then
 download=$(mktemp "$tools_dir/download.XXXXXX")
 trap 'rm -f "$download"' EXIT HUP INT TERM
 curl --fail --location --silent --show-error "https://github.com/tailwindlabs/tailwindcss/releases/download/$tailwind_version/tailwindcss-$platform" -o "$download"
 mv "$download" "$binary"
fi
if command -v sha256sum >/dev/null 2>&1; then
 actual=$(sha256sum "$binary" | cut -d ' ' -f 1)
else
 actual=$(shasum -a 256 "$binary" | cut -d ' ' -f 1)
fi
if [ "$actual" != "$digest" ]; then
 echo "Tailwind checksum mismatch: $binary" >&2
 exit 1
fi
chmod +x "$binary"
templ_dir="$tools_dir/templ-$templ_version"
if [ ! -x "$templ_dir/templ" ]; then
 mkdir -p "$templ_dir"
 GOBIN="$templ_dir" go install "github.com/a-h/templ/cmd/templ@$templ_version"
fi
"$templ_dir/templ" generate -path internal/web
"$binary" -i internal/web/styles.css -o internal/web/assets/app.css --minify
