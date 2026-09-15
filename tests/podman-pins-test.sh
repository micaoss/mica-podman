#!/usr/bin/env bash
# check-pins.sh against upstream release lists recorded under tests/podman-pins/
# (offline). Never writes locks/upstream.lock.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/.." && pwd)"
CHECK="${REPO_ROOT}/check-pins.sh"
FIX="${HERE}/podman-pins"
REAL_ENV="${REPO_ROOT}/locks/upstream.lock"

[ -x "${CHECK}" ] || { echo "error: ${CHECK} is missing or not executable" >&2; exit 1; }
[ -d "${FIX}/releases" ] || { echo "error: ${FIX}/releases not found; there is nothing to drive the check with" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

PASS_N=0
FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $1"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $1"; }

run_case() {
    local label="$1" env_file="$2" override="$3"
    local dir="${WORK}/${label}"
    mkdir -p "${dir}"
    cp "${FIX}/releases"/*.json "${dir}/"
    if [ -n "${override}" ]; then
        local src="${FIX}/overrides/${override%%:*}"
        local dest="${dir}/${override#*:}.json"
        [ -f "${src}" ] || { echo "error: override ${src} not found" >&2; exit 1; }
        cp "${src}" "${dest}"
    fi
    OUT_FILE="${WORK}/${label}.out"
    set +e
    bash "${CHECK}" --releases-dir "${dir}" --upstream-lock "${env_file}" >"${OUT_FILE}" 2>&1
    RC=$?
    set -e
}

expect_rc() {
    if [ "${RC}" -eq "$1" ]; then pass "$2 (exit ${RC})"; else
        fail "$2: expected exit $1, got ${RC}"
        sed 's/^/    /' "${OUT_FILE}"
    fi
}
expect_says() {
    if grep -c -- "$1" "${OUT_FILE}" >/dev/null; then pass "$2"; else
        fail "$2: output does not contain '$1'"
        sed 's/^/    /' "${OUT_FILE}"
    fi
}
expect_silent() {
    if grep -c -- "$1" "${OUT_FILE}" >/dev/null; then
        fail "$2: output should not contain '$1'"
        sed 's/^/    /' "${OUT_FILE}"
    else pass "$2"; fi
}

echo "--- 1. the tree as it stands is green"
run_case current "${REAL_ENV}" ""
expect_rc 0 "the six pins in locks/upstream.lock are current"
expect_says "RESULT: PASS" "the run says PASS"
for c in podman crun conmon netavark aardvark-dns catatonit; do
    expect_says "UNCHANGED  ${c} " "${c} is reported UNCHANGED by name"
done
expect_says "tag(s) skipped as a different convention" "a tag outside the pin's convention is skipped visibly"

echo
echo "--- 2. podman's 5.x line is respected, not overruled by 6.x"
expect_silent "BEHIND     podman" "podman is not called behind because a 6.x line exists"
expect_says "upstream also maintains v6.1.0 on the 6.x line" "the newer line is reported as a note"
expect_says "locks/upstream.lock's decision, not this check's" "the note says whose decision the line is"

echo
echo "--- 3. the catatonit case: quiet upstream, correctly pinned"
expect_says "catatonit     v0.2.1     newest upstream release, released 2024-12-14" \
    "a 2024 release date is reported, not read as a failure"
run_case catatonit-moved "${REAL_ENV}" "catatonit-moved.json:catatonit"
expect_rc 1 "catatonit goes red the moment upstream publishes v0.3.0"
expect_says "BEHIND     catatonit     v0.2.1     -> v0.3.0" "the red names catatonit, its pin and the newer tag"

echo
echo "--- 4. a pin edited backwards is red and says which"
run_case behind-conmon "${FIX}/upstream-lock/behind-conmon.lock" ""
expect_rc 1 "a conmon pin behind upstream fails the run"
expect_says "conmon: pinned at v2.1.13, upstream released v2.2.1" "the failure names the component, its pin and the newer tag"
expect_says "in locks/upstream.lock set the component's git row" "the failure text says to edit locks/upstream.lock"
expect_says "run \`make podman\`" "the failure text says to re-run make podman"
expect_says "git ls-remote" "the failure text says where the tag's commit comes from"
expect_says "moving a pin is a human act" "the failure text refuses to do the edit itself"
expect_silent "BEHIND     crun" "only the component that moved is reported behind"

echo
echo "--- 5. the same, for the pin that carries no v"
run_case behind-crun "${FIX}/upstream-lock/behind-crun.lock" ""
expect_rc 1 "a crun pin behind upstream fails the run"
expect_says "crun: pinned at 1.28, upstream released 1.29.1" "the unprefixed convention compares numerically"

echo
echo "--- 6. behind WITHIN the pinned line"
run_case behind-podman-line "${FIX}/upstream-lock/behind-podman-line.lock" ""
expect_rc 1 "a podman pin behind a newer 5.x release fails the run"
expect_says "podman: pinned at v5.8.3, upstream released v5.8.6" "the newer tag reported is the 5.x one"
expect_says "on the 5.x line" "the failure says which line it compared within"
expect_silent "released v6" "the 6.x line is never offered as the fix"

echo
echo "--- 7. prereleases and drafts are not releases"
run_case catatonit-prerelease "${REAL_ENV}" "catatonit-prerelease-only.json:catatonit"
expect_rc 0 "an rc and a draft above the pin do not make it behind"
expect_silent "v0.3.0-rc1" "the rc is not offered as a newer release"

echo
echo "--- 8. the check fails rather than passes when it cannot compare"
run_case reconventioned "${REAL_ENV}" "crun-reconventioned.json:crun"
expect_rc 2 "an upstream that changed its tag convention is an error, not a pass"
expect_says "changed how it tags or the pin did" "the error names the convention mismatch"

run_case empty "${REAL_ENV}" "catatonit-empty.json:catatonit"
expect_rc 2 "an upstream that returns no releases is an error, not a pass"
expect_says "holds no releases" "the error says the read came back empty"

run_case unknown-pin "${FIX}/upstream-lock/unknown-pin.lock" ""
expect_rc 2 "a pin upstream does not publish is an error, not a pass"
expect_says "is not among upstream's" "the error names the unpublished pin"

run_case missing-pin "${FIX}/upstream-lock/missing-pin.lock" ""
expect_rc 2 "a locks/upstream.lock missing a pin is an error, not a pass"
expect_says "has no git row for catatonit" "the error names the missing row"

echo
echo "--- 9. the check never writes the file it reads"
before="$(sha256sum "${REAL_ENV}" | cut -d' ' -f1)"
run_case readonly-proof "${REAL_ENV}" "catatonit-moved.json:catatonit"
after="$(sha256sum "${REAL_ENV}" | cut -d' ' -f1)"
if [ "${before}" = "${after}" ]; then
    pass "a run that found a pin behind left locks/upstream.lock byte-identical (${before:0:12})"
else
    fail "locks/upstream.lock changed under a check that is only allowed to read it"
fi

echo
if [ "${FAIL_N}" -eq 0 ]; then
    echo "RESULT: PASS (${PASS_N}/${PASS_N} assertions)"
else
    echo "RESULT: FAIL (${FAIL_N} of $((PASS_N + FAIL_N)) assertions failed)"
    exit 1
fi
