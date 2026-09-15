#!/usr/bin/env bash
# tools/version.sh against fixture control files: the declared version and
# SOURCE_DATE_EPOCH of mica-podman, and what it refuses. Offline.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
mkdir -p _out
TMP=$(mktemp -d "$REPO_ROOT/_out/version-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }
says() { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }

FIX="$TMP/repo"
mkdir -p "$FIX/tools" "$FIX/deb" "$FIX/locks"
cp tools/version.sh "$FIX/tools/"
printf '# mica-lock v1\ngit\tpodman\thttps://github.com/containers/podman.git\tv5.8.6\t%s\n' "$(printf 'a%.0s' {1..40})" >"$FIX/locks/upstream.lock"
control() { # <version line> <epoch line>
    printf 'Package: mica-podman\n%s\n%s\nArchitecture: @ARCH@\nMaintainer: Mica OS <hi@micaos.dev>\n' "$1" "$2" >"$FIX/deb/mica-podman.control"
}
run() { RC=0; OUT=$(bash "$FIX/tools/version.sh" "$@" 2>&1) || RC=$?; }

control 'Version: 5.8.6-1' 'X-Mica-Source-Date-Epoch: 1786640584'
run version
if [ "$RC" -eq 0 ] && [ "$OUT" = 5.8.6-1 ]; then pass "V1 the declared version is read"; else fail "V1 rc=$RC: $OUT"; fi
run epoch
if [ "$RC" -eq 0 ] && [ "$OUT" = 1786640584 ]; then pass "V2 the declared SOURCE_DATE_EPOCH is read"; else fail "V2 rc=$RC: $OUT"; fi

control 'Version: 5.8.6-3' 'X-Mica-Source-Date-Epoch: 1786640584'
run version
if [ "$RC" -eq 0 ] && [ "$OUT" = 5.8.6-3 ]; then pass "V3 a packaging revision bump is a version"; else fail "V3 rc=$RC: $OUT"; fi

control 'Version: 5.8.7-1' 'X-Mica-Source-Date-Epoch: 1786640584'
run version
if [ "$RC" -ne 0 ] && says "$OUT" "upstream part 5.8.7 is not the podman tag v5.8.6"; then pass "V4 an upstream part other than the podman tag is refused"; else fail "V4 rc=$RC: $OUT"; fi

for bad in 'Version: 5.8.6+git0123456789ab-1' 'Version: @VERSION@' 'Version: 5.8.6' 'Version: 1:5.8.6-1'; do
    control "$bad" 'X-Mica-Source-Date-Epoch: 1786640584'
    run version
    if [ "$RC" -ne 0 ] && says "$OUT" "is not <podman version>-<revision>"; then pass "V5 '$bad' is refused"; else fail "V5 '$bad' rc=$RC: $OUT"; fi
done

control 'Version: 5.8.6-1' 'X-Mica-Source-Date-Epoch: now'
run epoch
if [ "$RC" -ne 0 ] && says "$OUT" "X-Mica-Source-Date-Epoch"; then pass "V6 an epoch that is not whole seconds is refused"; else fail "V6 rc=$RC: $OUT"; fi

control 'Version: 5.8.6-1' 'Section: admin'
run epoch
if [ "$RC" -ne 0 ] && says "$OUT" "declares no X-Mica-Source-Date-Epoch"; then pass "V7 a missing epoch is refused"; else fail "V7 rc=$RC: $OUT"; fi

echo "RESULT: $FAIL_N failed, $PASS_N passed"
[ "$FAIL_N" -eq 0 ]
