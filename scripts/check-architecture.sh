#!/usr/bin/env bash
# Enforces the dependency direction of the packages below internal/core:
# a core context, its tests included, imports the standard library and its own
# context only. See docs/architecture.md.
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ ! -d internal/core ]]; then
    echo "architecture: no core package yet, nothing to check"
    exit 0
fi

module="$(go list -m)"
core_root="$module/internal/core"
imports_format='{{range .Imports}}{{.}}{{"\n"}}{{end}}{{range .TestImports}}{{.}}{{"\n"}}{{end}}{{range .XTestImports}}{{.}}{{"\n"}}{{end}}'
failed=0

# Lists are captured before looping so that a failing go list stops the
# script instead of giving an empty loop and a pass.
packages="$(go list ./internal/core/...)" || {
    echo "architecture: cannot list the core packages" >&2
    exit 1
}

while IFS= read -r package; do
    [[ -n "$package" ]] || continue
    relative="${package#"$core_root"}"
    relative="${relative#/}"
    context="${relative%%/*}"
    if [[ "$package" == "$core_root" ]]; then
        context=""
    fi

    imports="$(go list -f "$imports_format" "$package")" || {
        echo "architecture: cannot list the imports of $package" >&2
        exit 1
    }

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
    done < <(sort -u <<<"$imports")
done <<<"$packages"

# Test helpers are never linked into the binary. A test helper is a package of
# the module with a path element named golden or ending in test: the fake
# forge, the fixture repositories, the golden files and the contract suites of
# the core, and the next one that follows the convention.
deps="$(go list -deps ./cmd/raun)" || {
    echo "architecture: cannot list the dependencies of cmd/raun" >&2
    exit 1
}
while IFS= read -r dep; do
    [[ "$dep" == "$module"/* ]] || continue
    case "/${dep#"$module"/}/" in
        */golden/* | *test/*)
            echo "cmd/raun must not link the test helper $dep" >&2
            failed=1
            ;;
    esac
done <<<"$deps"

exit "$failed"
