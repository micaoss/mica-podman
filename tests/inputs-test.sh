#!/usr/bin/env bash
# tools/inputs.sh against fixture releases served over file:// and a fixture
# locks/ directory; then the tree itself: its locks/ passes the file rules and
# every third-party image it names is an upstream row of
# locks/mica-build-env.lock. Offline.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
mkdir -p _out
TMP=$(mktemp -d "$REPO_ROOT/_out/inputs-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }
says() { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }
sha() { sha256sum "$1" | cut -d' ' -f1; }
D() { printf '%s' "$1" | sha256sum | cut -d' ' -f1; }

SITE="$TMP/site"
LOCKS="$TMP/locks"
mkdir -p "$LOCKS/pins"
C40=0123456789abcdef0123456789abcdef01234567
# publish <repository> <release> <lock>: the release's two assets, and the lock and pin committed unchanged.
publish() {
    local dir="$SITE/micaoss/$1/releases/download/$2"
    mkdir -p "$dir"
    cp "$3" "$dir/$1.lock"
    (cd "$dir" && sha256sum "$1.lock" >SHA256SUMS)
    cp "$3" "$LOCKS/$1.lock"
    printf '# mica-pin v1\nREPOSITORY=%s\nRELEASE=%s\nSHA256SUMS=%s\n' "$1" "$2" "$(sha "$dir/SHA256SUMS")" >"$LOCKS/pins/$1.pin"
}
{
    printf '# mica-lock v1\nrelease\tmica-build-env\t20990101-0000\t%s\n' "$C40"
    for n in base c go rust; do
        printf 'image\tmica-build-env\t%s\tamd64\tghcr.io/micaoss/mica-build-env@sha256:%s\n' "$n" "$(D "$n-amd64")"
        printf 'image\tmica-build-env\t%s\tarm64\tghcr.io/micaoss/mica-build-env@sha256:%s\n' "$n" "$(D "$n-arm64")"
        printf 'image\tmica-build-env\t%s\tindex\tghcr.io/micaoss/mica-build-env:%s.20990101-0000@sha256:%s\n' "$n" "$n" "$(D "$n")"
    done
    printf 'image\tupstream\tregistry:3.1.1\tamd64\tdocker.io/library/registry:3.1.1@sha256:%s\n' "$(D registry)"
    printf 'image\tupstream\tregistry:3.1.1\tarm64\tdocker.io/library/registry:3.1.1@sha256:%s\n' "$(D registry)"
} >"$TMP/build-env.lock"
publish mica-build-env 20990101-0000 "$TMP/build-env.lock"
printf '# mica-lock v1\ngit\tpodman\thttps://github.com/containers/podman.git\tv1.0.0\t%s\n' "$C40" >"$LOCKS/upstream.lock"

run() {
    RC=0
    OUT=$(MICA_LOCKS="$LOCKS" MICA_RELEASE_DOWNLOAD="file://$SITE" bash tools/inputs.sh "$@" 2>&1) || RC=$?
}

run check
if [ "$RC" -eq 0 ]; then pass "I1 a locks/ directory that keeps the file rules passes"; else fail "I1 rc=$RC: $OUT"; fi

run verify
if [ "$RC" -eq 0 ] && says "$OUT" "mica-build-env 20990101-0000"; then pass "I2 a pinned release whose assets match passes"; else fail "I2 rc=$RC: $OUT"; fi

run image go
if [ "$RC" -eq 0 ] && [ "$OUT" = "ghcr.io/micaoss/mica-build-env:go.20990101-0000@sha256:$(D go)" ]; then
    pass "I3 image names the index reference of a build-env image"
else fail "I3 rc=$RC: $OUT"; fi

run upstream-image registry:3.1.1
if [ "$RC" -eq 0 ] && [ "$OUT" = "docker.io/library/registry:3.1.1@sha256:$(D registry)" ]; then
    pass "I4 upstream-image names an approved third-party image by its original reference"
else fail "I4 rc=$RC: $OUT"; fi

run upstream-image registry:2
if [ "$RC" -ne 0 ] && says "$OUT" "registry:2 is not an upstream image of locks/mica-build-env.lock"; then
    pass "I5 a third-party image the build-env lock does not list is refused"
else fail "I5 rc=$RC: $OUT"; fi

run image perl
if [ "$RC" -ne 0 ] && says "$OUT" "names no index of the image perl"; then pass "I6 an image the lock does not build is refused"; else fail "I6 rc=$RC: $OUT"; fi

PUB="$SITE/micaoss/mica-build-env/releases/download/20990101-0000"
cp "$PUB/SHA256SUMS" "$TMP/sums.bak"
echo "# republished" >>"$PUB/SHA256SUMS"
run verify
if [ "$RC" -ne 0 ] && says "$OUT" "the SHA256SUMS of mica-build-env 20990101-0000 hashes to $(sha "$PUB/SHA256SUMS"); locks/pins/mica-build-env.pin records $(sha "$TMP/sums.bak")"; then
    pass "I7 a release serving another SHA256SUMS than the pin is refused"
else fail "I7 rc=$RC: $OUT"; fi
cp "$TMP/sums.bak" "$PUB/SHA256SUMS"

cp "$PUB/mica-build-env.lock" "$TMP/lock.bak"
echo "# edited" >>"$PUB/mica-build-env.lock"
run verify
if [ "$RC" -ne 0 ] && says "$OUT" "mica-build-env.lock of mica-build-env 20990101-0000 downloads with other bytes"; then
    pass "I8 a release lock that downloads with other bytes is refused"
else fail "I8 rc=$RC: $OUT"; fi
cp "$TMP/lock.bak" "$PUB/mica-build-env.lock"

(cd "$PUB" && { cat SHA256SUMS; echo "$(D x)  extra"; } >SHA256SUMS.new && mv SHA256SUMS.new SHA256SUMS)
sed -i "s/^SHA256SUMS=.*/SHA256SUMS=$(sha "$PUB/SHA256SUMS")/" "$LOCKS/pins/mica-build-env.pin"
run verify
if [ "$RC" -ne 0 ] && says "$OUT" "does not list exactly mica-build-env.lock"; then pass "I9 a SHA256SUMS listing more than the lock is refused"; else fail "I9 rc=$RC: $OUT"; fi
cp "$TMP/sums.bak" "$PUB/SHA256SUMS"
sed -i "s/^SHA256SUMS=.*/SHA256SUMS=$(sha "$PUB/SHA256SUMS")/" "$LOCKS/pins/mica-build-env.pin"

sed -i 's/^RELEASE=.*/RELEASE=20990101-0001/' "$LOCKS/pins/mica-build-env.pin"
run image base
if [ "$RC" -ne 0 ] && says "$OUT" "refused release-mismatch"; then pass "I10 a locks/ directory breaking a pin rule is refused by its rule"; else fail "I10 rc=$RC: $OUT"; fi
sed -i 's/^RELEASE=.*/RELEASE=20990101-0000/' "$LOCKS/pins/mica-build-env.pin"

RC=0
OUT=$(bash tools/inputs.sh check 2>&1) || RC=$?
if [ "$RC" -eq 0 ]; then pass "I11 the tree's locks/ keeps the file rules"; else fail "I11 rc=$RC: $OUT"; fi

bad=""
while IFS= read -r ref; do
    name="${ref%@*}" name="${name#docker.io/}" name="${name#library/}"
    got="$(bash tools/inputs.sh upstream-image "${name}" 2>/dev/null || true)"
    [ -n "${got}" ] && [ "${got#*@}" = "${ref#*@}" ] || bad="${bad} ${ref}"
done < <(git ls-files | grep -v '^tests/vectors/\|^locks/' | xargs grep -ohE '(docker\.io/)?[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}' 2>/dev/null |
    grep -v 'mica-build-env\|mica-system-base\|mica-podman' | LC_ALL=C sort -u)
if [ -z "${bad}" ]; then pass "I12 every third-party image the tree names is an upstream row of locks/mica-build-env.lock, by original reference"; else fail "I12 not upstream rows:${bad}"; fi

echo "RESULT: $FAIL_N failed, $PASS_N passed"
[ "$FAIL_N" -eq 0 ]
