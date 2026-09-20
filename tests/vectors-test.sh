#!/usr/bin/env bash
# tools/vectors.sh against a synthetic `mica` served over file://: not mica's
# real vectors, because a fixture copy of them would be the copy this tool
# exists to remove. What is tested is the derivation -- what this repository
# reads, what it writes, and what its own forms may not be (9.1) -- the
# mica-vectors-pin v1 reader (9.2), and that a difference in either direction
# is refused. Offline.
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

# The synthetic mica. alpha publishes packages (the shape this repository has),
# beta images and -- in its own valid vector, not in any lock pinned below -- a
# data row, gamma releases under a scope. `board` is a kind only gamma carries.
CAN="$TMP/mica-$COMMIT/docs/design/release-lock/vectors"
mkdir -p "$CAN"
w() {
    local f="$CAN/$1"
    shift
    mkdir -p "$(dirname "$f")"
    {
        printf '# mica-lock v1\n'
        printf '%s\n' "$@"
    } >"$f"
}
ALPHA=$'release\talpha\t20260914-2042\t'"$C40"
BETA=$'release\tbeta\t20260914-2042\t'"$C40"
GAMMA=$'release\tgamma\tuefi-x64.20260914-2042\t'"$C40"
w lock/valid/alpha.lock "$ALPHA" $'pool\tamd64\tref' $'package\tp\tamd64\t1\tsha'
w lock/valid/beta.lock "$BETA" $'image\tbeta\tbase\tamd64\tref'
w lock/valid/beta-data.lock "$BETA" $'image\tbeta\tbase\tamd64\tref' $'data\tunowned\tunowned.tsv\tsha'
w lock/valid/gamma.uefi-x64.lock "$GAMMA" $'board\tuefi-x64\tamd64\tref'
w lock/refused/unknown-kind.lock "$ALPHA" $'source\tbun\tamd64\t1\tsha\thttps://x'
w lock/refused/data-file.lock "$BETA" $'data\tone\tsame.tsv\tsha' $'data\ttwo\tsame.tsv\tsha'
w lock/refused/scoped-release.lock $'release\talpha\tuefi-x64.20260914-2042\t'"$C40" $'pool\tamd64\tref'
w lock/refused/board-name.lock "$GAMMA" $'board\tOTHER\tamd64\tref'
w upstream/refused/other-kind.lock $'image\tupstream\td\tamd64\tref' $'pool\tamd64\tref'
w pins/valid/plain/alpha.lock "$ALPHA" $'pool\tamd64\tref'
w pins/valid/scoped/gamma.uefi-x64.lock "$GAMMA" $'board\tuefi-x64\tamd64\tref'
mkdir -p "$CAN/pins/valid/plain/pins" "$CAN/pins/valid/scoped/pins" "$CAN/vectors-pin/valid" "$CAN/vectors-pin/refused" "$CAN/repos/cache-hit"
printf '# mica-pin v1\nREPOSITORY=alpha\nRELEASE=20260914-2042\nSHA256SUMS=x\n' >"$CAN/pins/valid/plain/pins/alpha.pin"
printf '# mica-pin v1\nREPOSITORY=gamma\nSCOPE=uefi-x64\nRELEASE=20260914-2042\nSHA256SUMS=x\n' >"$CAN/pins/valid/scoped/pins/gamma.uefi-x64.pin"
printf '# mica-vectors-pin v1\nREPOSITORY=mica\nCOMMIT=%s\n' "$C40" >"$CAN/vectors-pin/valid/ok.pin"
printf '# mica-pin v1\nREPOSITORY=mica\nCOMMIT=%s\n' "$C40" >"$CAN/vectors-pin/refused/header.pin"
printf 'request\n' >"$CAN/repos/cache-hit/request"
{
    printf '# mica-vectors v1: path, result (valid|refused), rule, mode (ci|local|offline|-)\n'
    printf '%s\tvalid\t-\t-\n' lock/valid/alpha.lock lock/valid/beta.lock lock/valid/beta-data.lock lock/valid/gamma.uefi-x64.lock
    printf 'lock/refused/unknown-kind.lock\trefused\tkind-unknown\t-\n'
    printf 'lock/refused/data-file.lock\trefused\tdata-file\t-\n'
    printf 'lock/refused/scoped-release.lock\trefused\trelease-scope\t-\n'
    printf 'lock/refused/board-name.lock\trefused\tfield-value\t-\n'
    printf 'upstream/refused/other-kind.lock\trefused\tkind-unknown\t-\n'
    printf 'pins/valid/plain\tvalid\t-\tci\n'
    printf 'pins/valid/scoped\tvalid\t-\tci\n'
    printf 'vectors-pin/valid/ok.pin\tvalid\t-\t-\n'
    printf 'vectors-pin/refused/header.pin\trefused\theader\t-\n'
    printf 'repos/cache-hit\tvalid\t-\toffline\n'
} >"$CAN/expected.tsv"
{
    printf '# mica-vectors-derivation v1: refused vector, relation, the valid vector it is written against.\n'
    printf 'lock/refused/unknown-kind.lock\tedit-of\tlock/valid/alpha.lock\n'
    printf 'lock/refused/data-file.lock\tedit-of\tlock/valid/beta-data.lock\n'
    printf 'lock/refused/scoped-release.lock\tedit-of\tlock/valid/alpha.lock\n'
    printf 'lock/refused/board-name.lock\tedit-of\tlock/valid/gamma.uefi-x64.lock\n'
} >"$CAN/derived-from.tsv"
tar -czf "$SITE/$COMMIT" -C "$TMP" "mica-$COMMIT"

printf '# mica-vectors-pin v1\nREPOSITORY=mica\nCOMMIT=%s\n' "$COMMIT" >"$TMP/vectors.pin"
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
listed() { grep -v '^#' "$V/expected.tsv" | cut -f1 | LC_ALL=C sort | paste -sd' ' -; }

pins alpha=alpha beta=beta
run sync
WANT="lock/refused/data-file.lock lock/refused/scoped-release.lock lock/refused/unknown-kind.lock"
WANT="$WANT lock/valid/alpha.lock lock/valid/beta-data.lock lock/valid/beta.lock pins/valid/plain"
WANT="$WANT upstream/refused/other-kind.lock vectors-pin/refused/header.pin vectors-pin/valid/ok.pin"
if [ "$RC" -eq 0 ] && [ "$(listed)" = "$WANT" ]; then
    pass "V1 sync writes the forms this repository has, and no others"
else fail "V1 rc=$RC: $(listed) -- $OUT"; fi

run check
if [ "$RC" -eq 0 ] && says "$OUT" "10 vector(s), 4 not reachable here"; then
    pass "V2 the copy it just wrote passes, and the report counts both sides"
else fail "V2 rc=$RC: $OUT"; fi

# beta's pinned lock has no data row; its valid vector has one. The rows a
# producer's NEXT release may carry are the ones a consumer owes.
if [ -f "$V/lock/valid/beta-data.lock" ] && [ -f "$V/lock/refused/data-file.lock" ]; then
    pass "V3 a kind only the producer's valid vector carries is required before any pinned lock uses it"
else fail "V3 beta's data vectors are not in the copy"; fi

# The negative half: no pin is scoped, so the scoped vectors are not consumed
# here -- but the refusal of a scope in a lock of this shape still is.
if [ -f "$V/lock/refused/scoped-release.lock" ] && [ ! -e "$V/lock/valid/gamma.uefi-x64.lock" ]; then
    pass "V4 a refusal written against a form this repository has is required, though the form it refuses is not"
else fail "V4 the scoped refusal is missing or the scoped valid vector was copied"; fi

if [ ! -e "$V/lock/refused/board-name.lock" ] && [ ! -e "$V/pins/valid/scoped" ]; then
    pass "V5 a refusal written against a form this repository does not have is not required"
else fail "V5 a board-shaped vector was copied"; fi

if [ ! -e "$V/repos" ]; then pass "V6 a vector of a mode nothing here reads is never required"; else fail "V6 repos/ was copied"; fi

if [ -f "$V/vectors-pin/valid/ok.pin" ] && [ -f "$V/vectors-pin/refused/header.pin" ]; then
    pass "V7 the vectors-pin family is always required: this tool is its reader"
else fail "V7 the vectors-pin vectors were dropped"; fi

if [ -f "$V/lock/refused/unknown-kind.lock" ]; then
    pass "V8 a kind no valid lock of the format carries is the refusal, not a foreign form"
else fail "V8 unknown-kind.lock was dropped as a foreign kind"; fi

if grep -c '^lock/refused/data-file.lock' "$V/derived-from.tsv" >/dev/null &&
    ! grep -c '^lock/refused/board-name.lock' "$V/derived-from.tsv" >/dev/null; then
    pass "V9 the copy carries the sibling row of every refused vector it carries, and no other"
else fail "V9 derived-from.tsv does not follow the copy: $(cat "$V/derived-from.tsv")"; fi

# The set is derived, so pinning more changes it with no edit to any list.
pins alpha=alpha beta=beta gamma.uefi-x64=gamma:uefi-x64
run check
if [ "$RC" -ne 0 ] && says "$OUT" "gamma.uefi-x64.lock"; then
    pass "V10 pinning a scoped producer makes its vectors required, and the copy that lacks them is refused"
else fail "V10 rc=$RC: $OUT"; fi
run sync
if [ "$RC" -eq 0 ] && [ -f "$V/lock/valid/gamma.uefi-x64.lock" ] && [ -d "$V/pins/valid/scoped" ] &&
    [ -f "$V/lock/refused/board-name.lock" ]; then
    pass "V11 and sync writes them, the board refusal included"
else fail "V11 rc=$RC: $(listed) -- $OUT"; fi

pins alpha=alpha beta=beta
run sync
rm "$V/lock/valid/beta-data.lock"
run check
if [ "$RC" -ne 0 ] && says "$OUT" "beta-data.lock"; then pass "V12 a required vector missing here is refused"; else fail "V12 rc=$RC: $OUT"; fi

run sync
cp "$V/lock/valid/alpha.lock" "$V/lock/valid/stray.lock"
run check
if [ "$RC" -ne 0 ] && says "$OUT" "stray.lock"; then pass "V13 a vector here that mica does not have is refused"; else fail "V13 rc=$RC: $OUT"; fi

run sync
printf 'package\tq\tamd64\t1\tsha\n' >>"$V/lock/valid/alpha.lock"
run check
if [ "$RC" -ne 0 ] && says "$OUT" "alpha.lock"; then pass "V14 a required vector altered here is refused"; else fail "V14 rc=$RC: $OUT"; fi

run sync
pins alpha=alpha delta=delta
run check
if [ "$RC" -eq 1 ] && says "$OUT" "no valid lock vector of delta"; then
    pass "V15 a pinned producer mica has no valid vector for is a loud failure, not an empty subset"
else fail "V15 rc=$RC: $OUT"; fi

# 9.2, proved by the canonical vectors-pin family above and by this repository's own pin.
pins alpha=alpha beta=beta
printf '# mica-vectors-pin v1\nREPOSITORY=mica\nCOMMIT=%s\n' short >"$TMP/vectors.pin"
run check
if [ "$RC" -eq 1 ] && says "$OUT" "refused field-value"; then
    pass "V16 a pin whose commit is not a commit is refused before any fetch"
else fail "V16 rc=$RC: $OUT"; fi
printf '# mica-pin v1\nREPOSITORY=mica\nCOMMIT=%s\n' "$COMMIT" >"$TMP/vectors.pin"
run check
if [ "$RC" -eq 1 ] && says "$OUT" "refused header"; then
    pass "V17 a pin carrying another format's header is refused"
else fail "V17 rc=$RC: $OUT"; fi

RC=0
OUT=$(bash tools/vectors.sh pin "$REPO_ROOT/tools/vectors.pin" 2>&1) || RC=$?
if [ "$RC" -eq 0 ] && [ "$OUT" = valid ]; then
    pass "V18 this repository's own tools/vectors.pin is a mica-vectors-pin v1 record"
else fail "V18 rc=$RC: $OUT"; fi

echo "RESULT: ${FAIL_N} failed, ${PASS_N} passed"
[ "${FAIL_N}" -eq 0 ]
