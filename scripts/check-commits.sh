#!/usr/bin/env bash
# Checks every commit between a base revision and HEAD:
#   - the subject follows Conventional Commits: `type(scope)?!?: subject`,
#     lowercase type, subject starting with a lowercase letter, no trailing
#     period, at most 100 characters;
#   - the message carries no attribution trailer or tool mention.
# Usage: scripts/check-commits.sh <base>
set -euo pipefail

base="${1:?usage: $0 <base revision>}"

types='build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test'
subject_re="^(${types})(\([a-z0-9._/-]+\))?!?: [a-z0-9\`].*[^.]$"
attribution_re='^(co-authored-by|generated-by|assisted-by):|generated (with|by) '

failed=0
count=0

while IFS= read -r sha; do
    [[ -n "$sha" ]] || continue
    count=$((count + 1))
    subject="$(git log -1 --format=%s "$sha")"
    message="$(git log -1 --format=%B "$sha")"

    if ! grep -Eq "$subject_re" <<<"$subject"; then
        echo "${sha:0:12}: subject does not follow Conventional Commits: $subject" >&2
        failed=1
    fi
    if (( ${#subject} > 100 )); then
        echo "${sha:0:12}: subject longer than 100 characters: $subject" >&2
        failed=1
    fi
    if grep -Eiq "$attribution_re" <<<"$message"; then
        echo "${sha:0:12}: message carries an attribution line: $subject" >&2
        failed=1
    fi
done < <(git rev-list --no-merges "${base}..HEAD")

echo "commits: checked ${count} commit(s) since ${base}"
exit "$failed"
