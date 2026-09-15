#!/usr/bin/env bash
# tools/base-check.sh against fixtures served over file://: a mica-system-base
# release (its lock and SHA256SUMS), its rootfs layers in a registry layout, and
# a fixture resolver standing in for the apt run (MICA_BASE_RESOLVE); the apt
# run itself is exercised by `make base-check` in CI. Offline.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
mkdir -p _out
TMP=$(mktemp -d "$REPO_ROOT/_out/base-check-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }
says() { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }
sha() { sha256sum "$1" | cut -d' ' -f1; }
D() { printf '%s' "$1" | sha256sum | cut -d' ' -f1; }

REG="$TMP/registry"
SITE="$TMP/site"
FIX="$TMP/repo"
TAG=20990101-0000
C40=0123456789abcdef0123456789abcdef01234567
mkdir -p "$FIX/tools" "$FIX/deb" "$FIX/locks/pins" "$REG/v2/micaoss/mica-system-base/blobs" "$REG/v2/micaoss/mica-system-base/manifests"
cp tools/base-check.sh tools/inputs.sh tools/check-lock.sh "$FIX/tools/"
cp locks/mica-build-env.lock "$FIX/locks/"
cp locks/pins/mica-build-env.pin "$FIX/locks/pins/"
printf 'app\n' >"$FIX/deb/debian-depends"

rootfs() { # <arch> -> manifest digest of a one-layer root whose dpkg status names <arch>-root
    local d="$TMP/root-$1" ld m
    rm -rf "$d"
    mkdir -p "$d/var/lib/dpkg"
    printf 'Package: %s-root\nStatus: install ok installed\n\n' "$1" >"$d/var/lib/dpkg/status"
    tar -C "$d" -czf "$TMP/layer.tgz" ./var
    ld="sha256:$(sha "$TMP/layer.tgz")"
    cp "$TMP/layer.tgz" "$REG/v2/micaoss/mica-system-base/blobs/$ld"
    jq -n --arg d "$ld" '{schemaVersion: 2, layers: [{mediaType: "application/vnd.oci.image.layer.v1.tar+gzip", digest: $d}]}' >"$TMP/m.json"
    m="sha256:$(sha "$TMP/m.json")"
    cp "$TMP/m.json" "$REG/v2/micaoss/mica-system-base/manifests/$m"
    printf '%s' "$m"
}
AMD_ROOT=$(rootfs amd64)
ARM_ROOT=$(rootfs arm64)
URI=https://snapshot.debian.org/archive/debian/20260905T000000Z
row() { printf '%s\t%s\t%s\t%s\t%s/pool/main/%s_%s_%s.deb\n' "$1" "$2" "$3" "$(D "$1$2$3")" "$URI" "$1" "$3" "$2"; }

# The release: a lock whose upstream rows are <rows file> (row + roots), and its SHA256SUMS; lock and pin committed.
publish() { # <upstream rows file>
    local dir="$SITE/micaoss/mica-system-base/releases/download/$TAG"
    mkdir -p "$dir"
    {
        printf '# mica-lock v1\nrelease\tmica-system-base\t%s\t%s\n' "$TAG" "$C40"
        printf 'image\tmica-system-base\trootfs\tamd64\tghcr.io/micaoss/mica-system-base@%s\n' "$AMD_ROOT"
        printf 'image\tmica-system-base\trootfs\tarm64\tghcr.io/micaoss/mica-system-base@%s\n' "$ARM_ROOT"
        printf 'image\tmica-system-base\trootfs\tindex\tghcr.io/micaoss/mica-system-base:rootfs.%s@sha256:%s\n' "$TAG" "$(D index)"
        sed 's/^/upstream\t/' "$1" | LC_ALL=C sort
        printf 'apt\t%s\ttrixie\tmain\t/usr/share/keyrings/debian-archive-keyring.gpg\n' "$URI"
    } >"$dir/mica-system-base.lock"
    (cd "$dir" && sha256sum mica-system-base.lock >SHA256SUMS)
    cp "$dir/mica-system-base.lock" "$FIX/locks/"
    printf '# mica-pin v1\nREPOSITORY=mica-system-base\nRELEASE=%s\nSHA256SUMS=%s\n' "$TAG" "$(sha "$dir/SHA256SUMS")" >"$FIX/locks/pins/mica-system-base.pin"
}
# The fixture resolver: rows for the archives the root lacks, from $RESOLVED-<arch>, after asserting what apt would be given.
cat >"$TMP/resolve" <<'EOF2'
#!/usr/bin/env bash
set -euo pipefail
arch="$1" work="$2"
grep -c "^Package: ${arch}-root$" "$work/status-$arch" >/dev/null || { echo "resolver: not the $arch root status" >&2; exit 9; }
[ "$(ls "$work/sources.d")" = mica-system-base.sources ] || { echo "resolver: sources.d is not the one Base source" >&2; exit 9; }
[ "$(cat "$work/sources.d/mica-system-base.sources")" = "$(printf 'Types: deb\nURIs: %s\nSuites: trixie\nComponents: main\nCheck-Valid-Until: no\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg' "$URI")" ] ||
    { echo "resolver: the source is not the apt row rendered" >&2; exit 9; }
cat "$RESOLVED-$arch"
EOF2
chmod +x "$TMP/resolve"
run() {
    RC=0
    OUT=$(MICA_RELEASE_DOWNLOAD="file://$SITE" MICA_BASE_REGISTRY="file://$REG" MICA_BASE_RESOLVE="$TMP/resolve" RESOLVED="$TMP/resolved" URI="$URI" \
        bash "$FIX/tools/base-check.sh" 2>&1) || RC=$?
}
record() { # [rows file]: locks/upstream.lock with those rows as source rows
    { echo "# mica-lock v1"; [ "$#" -eq 0 ] || sed 's/^/source\t/' "$1" | LC_ALL=C sort; } >"$FIX/locks/upstream.lock"
}

{ printf '%s\tapp,other\n' "$(row liba amd64 1)"; printf '%s\tapp\n' "$(row liba arm64 1)"; } >"$TMP/base-rows"
publish "$TMP/base-rows"
row liba amd64 1 >"$TMP/resolved-amd64"
row liba arm64 1 >"$TMP/resolved-arm64"
record

run
if [ "$RC" -eq 0 ] && says "$OUT" "amd64: 1 archive(s) the root lacks, 1 pinned by mica-system-base, 0 recorded in locks/upstream.lock" && says "$OUT" "mica-system-base $TAG"; then
    pass "S1 archives the root lacks are all Base upstream rows for our roots, on both architectures, with the apt row as the only source"
else fail "S1 rc=$RC: $OUT"; fi

row libb arm64 2 >>"$TMP/resolved-arm64"
run
if [ "$RC" -ne 0 ] && says "$OUT" "libb	arm64	2	" && says "$OUT" "record it as a source row of locks/upstream.lock or propose it for Base"; then
    pass "S2 an archive Base does not pin and this repository does not record is refused with its row"
else fail "S2 rc=$RC: $OUT"; fi

row libb arm64 2 >"$TMP/recorded"
record "$TMP/recorded"
run
if [ "$RC" -eq 0 ] && says "$OUT" "arm64: 2 archive(s) the root lacks, 1 pinned by mica-system-base, 1 recorded in locks/upstream.lock"; then
    pass "S3 an archive recorded as a source row of locks/upstream.lock passes"
else fail "S3 rc=$RC: $OUT"; fi

row liba arm64 1 >"$TMP/resolved-arm64"
run
if [ "$RC" -ne 0 ] && says "$OUT" "locks/upstream.lock records libb	arm64	2" && says "$OUT" "no longer resolves"; then
    pass "S4 a recorded row that no longer resolves is refused"
else fail "S4 rc=$RC: $OUT"; fi
record

printf 'liba\tamd64\t9\t%s\t%s/pool/main/liba_9_amd64.deb\n' "$(D liba-other)" "$URI" >"$TMP/resolved-amd64"
run
if [ "$RC" -ne 0 ] && says "$OUT" "liba	amd64	9	"; then pass "S5 a resolved archive other than Base's row for that package is refused"; else fail "S5 rc=$RC: $OUT"; fi
row liba amd64 1 >"$TMP/resolved-amd64"

PUB="$SITE/micaoss/mica-system-base/releases/download/$TAG"
cp "$PUB/SHA256SUMS" "$TMP/sums.bak"
echo "# republished" >>"$PUB/SHA256SUMS"
run
if [ "$RC" -ne 0 ] && says "$OUT" "the SHA256SUMS of mica-system-base $TAG hashes to"; then pass "S6 a release serving another SHA256SUMS than the pin is refused"; else fail "S6 rc=$RC: $OUT"; fi
cp "$TMP/sums.bak" "$PUB/SHA256SUMS"

cp "$FIX/locks/mica-system-base.lock" "$TMP/lock.bak"
echo "# edited" >>"$FIX/locks/mica-system-base.lock"
run
if [ "$RC" -ne 0 ] && says "$OUT" "does not list exactly mica-system-base.lock at the sha256 of locks/mica-system-base.lock"; then
    pass "S7 an edited Base lock is refused"
else fail "S7 rc=$RC: $OUT"; fi
cp "$TMP/lock.bak" "$FIX/locks/mica-system-base.lock"

{ row liba amd64 1; row liba arm64 1; } >"$TMP/five-rows"
publish "$TMP/five-rows"
run
if [ "$RC" -ne 0 ] && says "$OUT" "refused column-count"; then pass "S8 a Base lock that breaks the file rules is refused by its rule"; else fail "S8 rc=$RC: $OUT"; fi
publish "$TMP/base-rows"

L=$(jq -r '.layers[0].digest' "$REG/v2/micaoss/mica-system-base/manifests/$ARM_ROOT")
cp "$REG/v2/micaoss/mica-system-base/blobs/$L" "$TMP/layer.bak" && printf X >>"$REG/v2/micaoss/mica-system-base/blobs/$L"
run
if [ "$RC" -ne 0 ] && says "$OUT" "does not hash to $L"; then pass "S9 a rootfs layer with other bytes is refused"; else fail "S9 rc=$RC: $OUT"; fi
cp "$TMP/layer.bak" "$REG/v2/micaoss/mica-system-base/blobs/$L"

run
if [ "$RC" -eq 0 ]; then pass "S10 the fixture passes again once restored"; else fail "S10 rc=$RC: $OUT"; fi

{ printf '%s\tother\n' "$(row liba amd64 1)"; printf '%s\tapp\n' "$(row liba arm64 1)"; } >"$TMP/other-rows"
publish "$TMP/other-rows"
run
if [ "$RC" -ne 0 ] && says "$OUT" "amd64: not pinned by mica-system-base $TAG for a root in deb/debian-depends" && says "$OUT" "liba	amd64	1	"; then
    pass "S11 an archive Base pins only for other roots is refused"
else fail "S11 rc=$RC: $OUT"; fi

echo "RESULT: $FAIL_N failed, $PASS_N passed"
[ "$FAIL_N" -eq 0 ]
