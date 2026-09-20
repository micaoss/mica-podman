#!/usr/bin/env bash
# tools/vectors.sh against a synthetic `mica` served over file://: not mica's
# real vectors, because a fixture copy of them would be the copy this tool
# exists to remove. What is tested is the derivation -- that the required
# subset follows from locks/pins/ and from what a pinned producer's own valid
# vectors carry, not from a list anyone edits -- and that a difference in
# either direction is refused. Offline.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
mkdir -p _out
TMP=$(mktemp -d "$REPO_ROOT/_out/vectors-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }
says() { case "$1" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

C40=0123456789abcdef0123456789abcdef01234567
COMMIT=abcdef0123456789abcdef0123456789abcdef01
SITE="$TMP/site"
V="$TMP/vectors"
mkdir -p "$SITE"

# The synthetic mica: alpha publishes packages, beta images and (in its own
# valid vector, not in any lock pinned below) a data row, gamma is the scoped
# producer. `pool` is a kind alpha carries, `board` one only gamma does.
CAN="$TMP/mica-$COMMIT/docs/design/release-lock/vectors"
mkdir -p "$CAN"/lock/valid "$CAN"/lock/refused "$CAN"/pins/valid/plain/pins "$CAN"/pins/valid/scoped/pins "$CAN"/repos/cache-hit
w() { local f="$CAN/$1"; shift; mkdir -p "$(dirname "$f")"; { printf '# mica-lock v1\n'; printf '%s\n' "$@"; } >"$f"; }
ALPHA_RELEASE=$'release\talpha\t20260914-2042\t'"$C40"
BETA_RELEASE=$'release\tbeta\t20260914-2042\t'"$C40"
GAMMA_RELEASE=$'release\tgamma\tuefi-x64.20260914-2042\t'"$C40"
w lock/valid/alpha.lock "$ALPHA_RELEASE" $'pool\tamd64\tref' $'package\tp\tamd64\t1\tsha'
w lock/valid/beta.lock "$BETA_RELEASE" $'image\tbeta\tbase\tamd64\tref'
w lock/valid/beta-data.lock "$BETA_RELEASE" $'image\tbeta\tbase\tamd64\tref' $'data\tunowned\tunowned.tsv\tsha'
w lock/valid/gamma.uefi-x64.lock "$GAMMA_RELEASE" $'board\tuefi-x64\tamd64\tref'
w lock/refused/unknown-kind.lock "$ALPHA_RELEASE" $'source\tbun\tamd64\t1\tsha\thttps://x'
w lock/refused/data-file.lock "$BETA_RELEASE" $'data\tone\tsame.tsv\tsha' $'data\ttwo\tsame.tsv\tsha'
w lock/refused/scoped-release.lock $'release\talpha\tuefi-x64.20260914-2042\t'"$C40" $'pool\tamd64\tref'
w pins/valid/plain/alpha.lock "$ALPHA_RELEASE" $'pool\tamd64\tref'
w pins/valid/scoped/gamma.uefi-x64.lock "$GAMMA_RELEASE" $'board\tuefi-x64\tamd64\tref'
printf '# mica-pin v1\nREPOSITORY=alpha\nRELEASE=20260914-2042\nSHA256SUMS=x\n' >"$CAN/pins/valid/plain/pins/alpha.pin"
printf '# mica-pin v1\nREPOSITORY=gamma\nSCOPE=uefi-x64\nRELEASE=20260914-2042\nSHA256SUMS=x\n' >"$CAN/pins/valid/scoped/pins/gamma.uefi-x64.pin"
printf 'request\n' >"$CAN/repos/cache-hit/request"
{
    printf '# mica-vectors v1: path, result (valid|refused), rule, mode (ci|local|offline|-)\n'
    printf '%s\tvalid\t-\t-\n' lock/valid/alpha.lock lock/valid/beta.lock lock/valid/beta-data.lock lock/valid/gamma.uefi-x64.lock
    printf 'lock/refused/unknown-kind.lock\trefused\tkind-unknown\t-\n'
    printf 'lock/refused/data-file.lock\trefused\tdata-file\t-\n'
    printf 'lock/refused/scoped-release.lock\trefused\trelease-scope\t-\n'
    printf 'pins/valid/plain\tvalid\t-\tci\n'
    printf 'pins/valid/scoped\tvalid\t-\tci\n'
    printf 'repos/cache-hit\tvalid\t-\toffline\n'
} >"$CAN/expected.tsv"
tar -czf "$SITE/$COMMIT" -C "$TMP" "mica-$COMMIT"

printf '# mica-vectors v1\nREPOSITORY=mica\nCOMMIT=%s\n' "$COMMIT" >"$TMP/vectors.pin"
# pins <name>=<repository>[:<scope>] ...: a fixture locks/pins/ directory.
pins() {
    local p name repository scope
    rm -rf "$TMP/locks"
    mkdir -p "$TMP/locks/pins"
    for p in "$@"; do
        name="${p%%=*}" repository="${p#*=}" scope=""
        case "$repository" in *:*) scope="${repository#*:}" repository="${repository%%:*}" ;; esac
        {
            printf '# mica-pin v1\nREPOSITORY=%s\n' "$repository"
            [ -z "$scope" ] || printf 'SCOPE=%s\n' "$scope"
            printf 'RELEASE=20260914-2042\nSHA256SUMS=x\n'
        } >"$TMP/locks/pins/$name.pin"
    done
}
run() {
    RC=0
    OUT=$(MICA_VECTORS="$V" MICA_VECTORS_PIN="$TMP/vectors.pin" MICA_LOCKS="$TMP/locks" \
        MICA_VECTORS_SOURCE="file://$SITE" bash tools/vectors.sh "$@" 2>&1) || RC=$?
}
listed() { LC_ALL=C sort <"$V/expected.tsv" | grep -v '^#' | cut -f1 | paste -sd' ' -; }

pins alpha=alpha beta=beta
run sync
if [ "$RC" -eq 0 ] && [ "$(listed)" = "lock/refused/data-file.lock lock/refused/unknown-kind.lock lock/valid/alpha.lock lock/valid/beta-data.lock lock/valid/beta.lock pins/valid/plain" ]; then
    pass "V1 sync writes the vectors the pinned producers can produce, and no others"
else fail "V1 rc=$RC: $(listed) -- $OUT"; fi

run check
if [ "$RC" -eq 0 ] && says "$OUT" "6 vector(s), 4 not reachable here"; then pass "V2 the copy it just wrote passes, and the report counts both sides"; else fail "V2 rc=$RC: $OUT"; fi

# beta's pinned lock has no data row; its valid vector does. The rows a
# producer's NEXT release may carry are the ones a consumer owes.
if [ -f "$V/lock/valid/beta-data.lock" ] && [ -f "$V/lock/refused/data-file.lock" ]; then
    pass "V3 a kind only the producer's valid vector carries is required before any pinned lock uses it"
else fail "V3 beta's data vectors are not in the copy"; fi

if [ ! -e "$V/lock/valid/gamma.uefi-x64.lock" ] && [ ! -e "$V/lock/refused/scoped-release.lock" ] && [ ! -e "$V/pins/valid/scoped" ]; then
    pass "V4 a kind and a scope no pin reaches are not required"
else fail "V4 the scoped and board vectors are in a copy that pins neither"; fi

if [ ! -e "$V/repos" ]; then pass "V5 a vector of a mode this repository's checker does not have is never required"; else fail "V5 repos/ was copied"; fi

if [ -f "$V/lock/refused/unknown-kind.lock" ]; then
    pass "V6 a kind no producer of the format emits is the refusal, not the form: unknown-kind stays required"
else fail "V6 unknown-kind.lock was dropped as a foreign kind"; fi

# The set is derived, so pinning more changes it with no edit to any list.
pins alpha=alpha beta=beta gamma.uefi-x64=gamma:uefi-x64
run check
if [ "$RC" -ne 0 ] && says "$OUT" "gamma.uefi-x64.lock"; then
    pass "V7 pinning a scoped producer makes its vectors required, and the copy that lacks them is refused"
else fail "V7 rc=$RC: $OUT"; fi
run sync
if [ "$RC" -eq 0 ] && [ -f "$V/lock/valid/gamma.uefi-x64.lock" ] && [ -d "$V/pins/valid/scoped" ] && [ -f "$V/lock/refused/scoped-release.lock" ]; then
    pass "V8 and sync writes them"
else fail "V8 rc=$RC: $(listed) -- $OUT"; fi

pins alpha=alpha beta=beta
run sync
rm "$V/lock/valid/beta-data.lock"
run check
if [ "$RC" -ne 0 ] && says "$OUT" "beta-data.lock"; then pass "V9 a required vector missing here is refused"; else fail "V9 rc=$RC: $OUT"; fi

run sync
cp "$V/lock/valid/alpha.lock" "$V/lock/valid/stray.lock"
run check
if [ "$RC" -ne 0 ] && says "$OUT" "stray.lock"; then pass "V10 a vector here that mica does not have is refused"; else fail "V10 rc=$RC: $OUT"; fi

run sync
printf 'package\tq\tamd64\t1\tsha\n' >>"$V/lock/valid/alpha.lock"
run check
if [ "$RC" -ne 0 ] && says "$OUT" "alpha.lock"; then pass "V11 a required vector altered here is refused"; else fail "V11 rc=$RC: $OUT"; fi

run sync
pins alpha=alpha delta=delta
run check
if [ "$RC" -eq 1 ] && says "$OUT" "no valid lock vector of delta"; then
    pass "V12 a pinned producer mica has no valid vector for is a loud failure, not an empty subset"
else fail "V12 rc=$RC: $OUT"; fi

pins alpha=alpha beta=beta
printf '# mica-vectors v1\nREPOSITORY=mica\nCOMMIT=%s\n' short >"$TMP/vectors.pin"
run check
if [ "$RC" -eq 1 ] && says "$OUT" "full 40-character object name"; then pass "V13 a pin that is not a commit is refused before any fetch"; else fail "V13 rc=$RC: $OUT"; fi

echo "RESULT: ${FAIL_N} failed, ${PASS_N} passed"
[ "${FAIL_N}" -eq 0 ]
