#!/usr/bin/env bash
# tools/check-lock.sh, and tools/vectors.sh for the vectors-pin family, over
# the specification's vectors: tests/vectors/ is
# mica:docs/design/release-lock/vectors/ at the commit tools/vectors.pin names,
# less the forms this repository cannot encounter. tools/vectors.sh owns both
# halves of that sentence and `make vectors` refuses a difference; this test
# only runs the checker over what is there. The path's first directory names
# the mode; a pins vector runs under CI when its mode is `ci`. Offline.
set -euo pipefail
cd "$(dirname "$0")/.."
V=tests/vectors
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }

listed=()
while IFS=$'\t' read -r path result rule mode; do
    case "${path}" in '#'* | '') continue ;; esac
    listed+=("${path}")
    want=valid
    [ "${result}" = valid ] || want="refused ${rule}"
    case "${path%%/*}" in
    vectors-pin) got="$(bash tools/vectors.sh pin "${V}/${path}" 2>&1 || true)" ;;
    *)
        if [ "${mode}" = ci ]; then
            got="$(CI=true bash tools/check-lock.sh "${path%%/*}" "${V}/${path}" 2>&1 || true)"
        else
            got="$(env -u CI -u GITHUB_ACTIONS bash tools/check-lock.sh "${path%%/*}" "${V}/${path}" 2>&1 || true)"
        fi
        ;;
    esac
    if [ "${got}" = "${want}" ]; then pass "${path}: ${want}"; else fail "${path}: expected '${want}', got '${got}'"; fi
done <"${V}/expected.tsv"

# Every vector is listed: a lock or upstream file, or a pins directory.
while IFS= read -r path; do
    printf '%s\n' "${listed[@]}" | grep -Fx -- "${path}" >/dev/null || fail "${path} is a vector expected.tsv does not list"
done < <(cd "${V}" && { find lock upstream vectors-pin -type f; find pins -mindepth 2 -maxdepth 2 -type d; } | LC_ALL=C sort)

# 9.3: every refused vector here names the valid vector it was written against,
# and that vector is here and passes. It bounds where a refusal can come from:
# the surroundings of the defect are a lock this reader accepts, so the refusal
# is the edit. It does not prove the edit breaks one rule and not two -- that
# needs the edited line read, and `a refused vector that could be refused by two
# rules tests neither` (mica-core, 2026-09-20).
declare -A RESULT_OF=()
while IFS=$'\t' read -r path result rule mode; do
    case "${path}" in '#'* | '') continue ;; esac
    RESULT_OF["${path}"]="${result}"
done <"${V}/expected.tsv"
siblings=0
while IFS=$'\t' read -r refused relation sibling; do
    case "${refused}" in '#'* | '') continue ;; esac
    siblings=$((siblings + 1))
    [ -n "${RESULT_OF[${refused}]-}" ] || fail "derived-from.tsv names ${refused}, which expected.tsv does not list"
    if [ "${RESULT_OF[${sibling}]-}" = valid ]; then
        pass "${refused}: ${relation} ${sibling}, which is here and valid"
    else
        fail "${refused} is written against ${sibling}, which is not a valid vector of this copy"
    fi
done <"${V}/derived-from.tsv"
[ "${siblings}" -gt 0 ] || fail "derived-from.tsv names no sibling; every refused lock vector declares one"

echo "RESULT: ${FAIL_N} failed, ${PASS_N} passed"
[ "${FAIL_N}" -eq 0 ]
