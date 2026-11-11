#!/usr/bin/env bash
# Enforces the dependency direction of the packages below internal/core:
# a core context imports the standard library and its own context only.
# See docs/architecture.md.
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ ! -d internal/core ]]; then
    echo "architecture: no core package yet, nothing to check"
    exit 0
fi

module="$(go list -m)"
core_root="$module/internal/core"
failed=0

while IFS= read -r package; do
    relative="${package#"$core_root"}"
    relative="${relative#/}"
    context="${relative%%/*}"
    if [[ "$package" == "$core_root" ]]; then
        context=""
    fi

    while IFS= read -r imported; do
        [[ -n "$imported" ]] || continue

        if [[ "$imported" == "$core_root/"* ]]; then
            imported_relative="${imported#"$core_root/"}"
            imported_context="${imported_relative%%/*}"
            if [[ -z "$context" || "$imported_context" != "$context" ]]; then
                echo "$package must not import another core context: $imported" >&2
                failed=1
            fi
            continue
        fi

        if [[ "$imported" == "$module/"* ]]; then
            echo "$package must not import outside its core context: $imported" >&2
            failed=1
            continue
        fi

        if [[ "$(go list -f '{{.Standard}}' "$imported")" != "true" ]]; then
            echo "$package must not import a non-standard package: $imported" >&2
            failed=1
        fi
    done < <(go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' "$package")
done < <(go list ./internal/core/...)

exit "$failed"
