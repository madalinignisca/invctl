#!/usr/bin/env bash
# invctl — infrastructure inventory
# Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
#
# Licensed under the GNU Affero General Public License, version 3 only —
# no later version applies. See LICENSE for the full text.
#
# SPDX-License-Identifier: AGPL-3.0-only
#
# Which manual fragments describe code that has changed since they were written.
#
# THE POINT OF THE WHOLE ARRANGEMENT. A manual regenerated from scratch on every
# change is a manual nobody regenerates, so each fragment declares the source
# paths it describes and the commit it was written against. This turns "is the
# manual current" from a judgement into a question with an answer.
#
# Exit codes: 0 everything current, 1 some fragments stale, 2 the manifest or
# the repo is not in a state where the question can be answered. The third is
# deliberately distinct -- "I could not tell" must never look like "all fine".

set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
MANIFEST="docs/manual/MANIFEST.yaml"

[ -f "$MANIFEST" ] || { echo "no $MANIFEST" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git repository" >&2; exit 2; }

verbose=0
[ "${1:-}" = "-v" ] && verbose=1

stale=0
checked=0
id=""; sha=""; file=""; paths=""; shots=""; section=""

flush() {
    [ -z "$id" ] && return 0
    checked=$((checked + 1))

    if [ -z "$sha" ]; then
        echo "STALE  $id — never generated"
        stale=$((stale + 1))
        return 0
    fi
    if ! git cat-file -e "${sha}^{commit}" 2>/dev/null; then
        # A rewritten history or a fragment copied from elsewhere. Reporting it
        # as current would be a guess; reporting it as stale sends somebody to
        # look, which is the right outcome.
        echo "STALE  $id — generated_at $sha is not a commit here"
        stale=$((stale + 1))
        return 0
    fi
    if [ -n "$file" ] && [ ! -f "docs/manual/$file" ]; then
        echo "STALE  $id — $file is missing"
        stale=$((stale + 1))
        return 0
    fi

    # A fragment whose images are gone is stale whatever the code did. This
    # catches the manifest claiming a screenshot nobody captured, which is the
    # dishonest failure -- the prose reads as complete and an image is missing.
    for shot in $shots; do
        if [ ! -f "docs/manual/$shot" ]; then
            echo "STALE  $id — screenshot $shot is missing"
            stale=$((stale + 1))
            return 0
        fi
    done

    # shellcheck disable=SC2086
    changed=$(git diff --name-only "$sha..HEAD" -- $paths 2>/dev/null)
    if [ -n "$changed" ]; then
        echo "STALE  $id"
        if [ "$verbose" = 1 ]; then
            echo "$changed" | sed 's/^/         /'
        fi
        stale=$((stale + 1))
    elif [ "$verbose" = 1 ]; then
        echo "ok     $id"
    fi
}

# Parsed with sed rather than a yaml library: this has to run on a machine that
# may have neither python-yaml nor yq, and the manifest is deliberately written
# in the subset that makes that safe.
while IFS= read -r line; do
    case "$line" in
        "  - id: "*)
            flush
            id="${line#  - id: }"; sha=""; file=""; paths=""; shots=""; section=""
            ;;
        "    generated_at: "*) sha="${line#    generated_at: }" ;;
        "    file: "*)         file="${line#    file: }" ;;
        "    depends_on:"*)    section="deps" ;;
        "    screenshots:"*)   section="shots" ;;
        "    pages:"*|"    preconditions:"*|"    screenshots_pending:"*) section="" ;;
        "      - "*)
            [ "$section" = "deps" ]  && paths="$paths ${line#      - }"
            [ "$section" = "shots" ] && shots="$shots ${line#      - }"
            ;;
    esac
done < "$MANIFEST"
flush

if [ "$checked" -eq 0 ]; then
    echo "the manifest lists no fragments; this check asserted nothing" >&2
    exit 2
fi

echo
if [ "$stale" -eq 0 ]; then
    echo "$checked fragment(s), all current."
    exit 0
fi
echo "$stale of $checked fragment(s) stale. Regenerate only those — see docs/manual/REGENERATING.md."
exit 1
