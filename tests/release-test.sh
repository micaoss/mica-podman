#!/usr/bin/env bash
# tools/release.sh <tag> against a stub gh (a release store in a directory,
# served over file://), a bare git remote for the tags and a real registry: a
# sibling registry container, reached on 127.0.0.1 or, from a container on the
# traefik network, by name. Needs docker; no other network.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
mkdir -p _out
TMP=$(mktemp -d "$REPO_ROOT/_out/release-test.XXXXXX")
REGISTRY_IMAGE="$(bash tools/inputs.sh upstream-image registry:3.1.1)"
NAME="ai-agent-mica-podman-registry-$$"
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$TMP"' EXIT
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }
says() { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }

net=()
if docker network inspect traefik >/dev/null 2>&1; then net=(--network traefik); fi
docker run -d --rm --label ai-agent=true --name "$NAME" "${net[@]}" -p 127.0.0.1::5000 "$REGISTRY_IMAGE" >/dev/null
PORT=$(docker port "$NAME" 5000/tcp | head -n1 | sed 's/.*://')
REG=""
for _ in $(seq 1 30); do
    for url in "http://127.0.0.1:$PORT" "http://$NAME:5000"; do
        if curl -sf --max-time 2 -o /dev/null "$url/v2/"; then REG=$url; break 2; fi
    done
    sleep 1
done
[ -n "$REG" ] || { echo "error: the registry $NAME did not answer" >&2; exit 1; }
POOL=micaoss/mica-podman
manifest() { curl -sf -H 'Accept: application/vnd.oci.image.manifest.v1+json' "$REG/v2/$POOL/manifests/$1"; } # <tag|digest>
nopool() { ! curl -sf -o /dev/null -H 'Accept: application/vnd.oci.image.manifest.v1+json' "$REG/v2/$POOL/manifests/pool.amd64.$1"; } # <tag>

STORE="$TMP/store"
BARE="$TMP/remote.git"
mkdir -p "$STORE/.meta" "$TMP/bin"
export STORE
git init -q --bare "$BARE"

# The stub: `api repos/<slug>/releases/tags/<tag>` and `release upload <tag> <files> -R <slug>`.
cat >"$TMP/bin/gh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >>"$STORE/calls"
case "$1" in
api)
    case "$2" in */releases\?*) jq -s . "$STORE"/.meta/*.json; exit 0 ;; esac
    tag="${2##*/}"
    [ -f "$STORE/.meta/$tag.json" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    cat "$STORE/.meta/$tag.json"
    ;;
release)
    [ "$2" = upload ] || exit 2
    tag="$3"
    shift 3
    files=()
    while [ "$#" -gt 0 ]; do
        case "$1" in -R) shift 2 ;; --clobber) echo "clobber used" >&2; exit 3 ;; *) files+=("$1"); shift ;; esac
    done
    meta="$STORE/.meta/$tag.json"
    for f in "${files[@]}"; do
        n="$(basename "$f" | tr '+' '.')"
        jq -e --arg n "$n" 'any(.assets[]; .name == $n)' "$meta" >/dev/null && { echo "asset under the same name already exists: $n" >&2; exit 1; }
        cp "$f" "$STORE/$tag/$n"
        [ -z "${CORRUPT:-}" ] || case "$n" in *.lock) printf X | dd of="$STORE/$tag/$n" bs=1 count=1 conv=notrunc 2>/dev/null ;; esac
        jq --arg n "$n" --argjson s "$(stat -c %s "$f")" --arg d "sha256:$(sha256sum "$f" | cut -d' ' -f1)" \
            '.assets += [{name: $n, size: $s, digest: $d, state: "uploaded"}]' "$meta" >"$meta.new"
        mv "$meta.new" "$meta"
    done
    ;;
*) exit 2 ;;
esac
STUB
chmod +x "$TMP/bin/gh"

FIX="$TMP/repo"
mkdir -p "$FIX/tools"
mkdir -p "$FIX/deb" "$FIX/locks" "$FIX/overlay"
cp tools/release.sh tools/check-lock.sh tools/version.sh tools/reuse.sh tools/package-inputs.sh "$FIX/tools/"
cp deb/mica-podman.control "$FIX/deb/"
cp locks/upstream.lock "$FIX/locks/"
echo fixture >"$FIX/overlay/fixture.conf"
printf '_out/\n' >"$FIX/.gitignore"
git -C "$FIX" init -q
git -C "$FIX" remote add origin https://github.com/micaoss/mica-podman.git
git -C "$FIX" -c user.name=f -c user.email=f@invalid add -A
git -C "$FIX" -c user.name=f -c user.email=f@invalid commit -qm one
OTHER=$(git -C "$FIX" rev-parse HEAD)
git -C "$FIX" -c user.name=f -c user.email=f@invalid commit -qm two --allow-empty
COMMIT=$(git -C "$FIX" rev-parse HEAD)
git -C "$FIX" update-ref refs/remotes/origin/main "$COMMIT"
git -C "$FIX" push -q "$BARE" HEAD:refs/heads/main
V="$(bash tools/version.sh version)"
TAG=20260914-0100
LOCK=mica-podman.lock

deb() { # <arch> [version] [extra control line] [payload] [name]
    local arch="$1" version="${2:-$V}" extra="${3:-}" payload="${4:-payload}"
    local d="$TMP/pkg-$arch" out="$FIX/_out/debs/$arch/pool"
    rm -rf "$d"
    mkdir -p "$d/DEBIAN" "$d/usr/share/mica-podman" "$out"
    printf '%s\n' "$payload" >"$d/usr/share/mica-podman/fixture"
    printf 'Package: mica-podman\nVersion: %s\nArchitecture: %s\nMaintainer: Mica OS <hi@micaos.dev>\nDescription: fixture\nMica-Source-Repo: mica-podman\n%s' \
        "$version" "$arch" "${extra:+$extra
}" >"$d/DEBIAN/control"
    find "$d" -exec touch -h -d @1700000000 {} +
    SOURCE_DATE_EPOCH=1700000000 dpkg-deb --build --root-owner-group "$d" "$out/${5:-mica-podman_${version}_${arch}.deb}" >/dev/null
}
reset_debs() { rm -rf "$FIX/_out/debs"; deb amd64; deb arm64; }
# A release as the user cuts it with `gh release create <tag> --target <commit>`: tag and empty release.
cut() { # <tag> [commit] [draft] [repository]
    local tag="$1" commit="${2:-$COMMIT}" draft="${3:-false}" repo="${4:-micaoss/mica-podman}"
    rm -rf "$STORE/${tag:?}"
    mkdir -p "$STORE/$tag"
    jq -n --arg t "$tag" --arg c "$commit" --argjson d "$draft" --arg r "$repo" \
        '{tag_name: $t, draft: $d, prerelease: false, target_commitish: $c, html_url: ("https://github.com/" + $r + "/releases/tag/" + $t), assets: []}' \
        >"$STORE/.meta/$tag.json"
    git -C "$BARE" tag -f "$tag" "$commit" >/dev/null
}
release() { # <tag> [token]
    RC=0
    rm -f "$STORE/calls"
    OUT=$(PATH="$TMP/bin:$PATH" GH_TOKEN="${2-fixture-token}" MICA_RELEASE_DOWNLOAD="file://$STORE" MICA_RELEASE_GIT="$BARE" MICA_OCI_REGISTRY="$REG" \
        bash "$FIX/tools/release.sh" ${1:+"$1"} 2>&1) || RC=$?
    ! says "$OUT" "fixture-token" || fail "the token appeared in the output"
}
calls() { cat "$STORE/calls" 2>/dev/null || true; }
uploads() { calls | grep -c '^release upload' || true; }

# The pool manifest of <arch>: the archive as its one layer, with the source annotations.
pooled() { # <arch>
    local sha m
    sha=$(sha256sum "$FIX/_out/debs/$1/pool/mica-podman_${V}_$1.deb" | awk '{print $1}')
    m=$(manifest "pool.$1.$TAG") || return 1
    jq -e --arg t "mica-podman_${V}_$1.deb" --arg d "sha256:$sha" --arg a "$1" --arg i "$(bash "$FIX/tools/package-inputs.sh" "$1")" \
        '.artifactType == "application/vnd.mica.pool" and (.layers | length) == 1 and .layers[0].digest == $d and
         .layers[0].mediaType == "application/vnd.mica.deb" and .layers[0].annotations["org.opencontainers.image.title"] == $t and
         .layers[0].annotations["mica.inputs"] == $i and
         .annotations == {"mica.source-repo": "mica-podman", "mica.arch": $a}' <<<"$m" >/dev/null
}
# The lock the release must carry: its rows, from the archives and the pools the registry holds.
lockrows() {
    local a
    printf 'release\tmica-podman\t%s\t%s\n' "$TAG" "$COMMIT"
    for a in amd64 arm64; do
        printf 'pool\t%s\tghcr.io/%s:pool.%s.%s@sha256:%s\n' "$a" "$POOL" "$a" "$TAG" "$(manifest "pool.$a.$TAG" | sha256sum | awk '{print $1}')"
    done
    for a in amd64 arm64; do
        printf 'package\tmica-podman\t%s\t%s\t%s\n' "$a" "$V" "$(sha256sum "$FIX/_out/debs/$a/pool/mica-podman_${V}_$a.deb" | awk '{print $1}')"
    done
}

reset_debs
cut "$TAG"
release "$TAG"
if [ "$RC" -eq 0 ] && [ "$(uploads)" -ge 1 ] && ! says "$(calls)" "release create" &&
    [ "$(ls "$STORE/$TAG" | LC_ALL=C sort | tr '\n' ' ')" = "SHA256SUMS $LOCK " ] &&
    [ "$(cat "$STORE/$TAG/SHA256SUMS")" = "$(sha256sum "$STORE/$TAG/$LOCK" | awk '{print $1}')  $LOCK" ] &&
    [ "$(head -n1 "$STORE/$TAG/$LOCK")" = "# mica-lock v1" ] && [ "$(grep -v '^#' "$STORE/$TAG/$LOCK")" = "$(lockrows)" ] &&
    [ "$(bash tools/check-lock.sh lock "$STORE/$TAG/$LOCK")" = valid ] && pooled amd64 && pooled arm64 &&
    says "$OUT" "downloaded anonymously"; then
    pass "R1 a published empty release receives exactly mica-podman.lock and SHA256SUMS, after both pools read back anonymously"
else fail "R1 rc=$RC: $OUT"; fi

AMD_MANIFEST=$(manifest "pool.amd64.$TAG" | sha256sum || true)
release "$TAG"
if [ "$RC" -eq 0 ] && [ "$(uploads)" -eq 0 ] && ! says "$OUT" "pushed ghcr.io" && says "$OUT" "downloaded anonymously" && [ "$(manifest "pool.amd64.$TAG" | sha256sum)" = "$AMD_MANIFEST" ]; then
    pass "R2 a rerun over the same assets and pools uploads nothing, keeps the manifests and reads back"
else fail "R2 rc=$RC: $OUT"; fi

cp "$STORE/.meta/$TAG.json" "$TMP/full.json"
jq --arg n "$LOCK" '.assets |= map(select(.name == $n))' "$TMP/full.json" >"$STORE/.meta/$TAG.json"
rm -f "$STORE/$TAG/SHA256SUMS"
release "$TAG"
if [ "$RC" -eq 0 ] && ! says "$(calls)" "/$LOCK" && says "$(calls)" "SHA256SUMS" && [ -f "$STORE/$TAG/SHA256SUMS" ]; then
    pass "R3 a partial release receives only the missing assets"
else fail "R3 rc=$RC: $OUT"; fi

deb amd64 "$V" "" other
release "$TAG"
if [ "$RC" -ne 0 ] && says "$OUT" "$LOCK is already attached with other bytes" && [ "$(uploads)" -eq 0 ]; then
    pass "R4 an attached asset with other bytes is refused, not replaced"
else fail "R4 rc=$RC: $OUT"; fi
reset_debs

cp "$STORE/.meta/$TAG.json" "$TMP/full.json"
jq '.assets += [{name: "extra.txt", size: 1, digest: "sha256:00", state: "uploaded"}]' "$TMP/full.json" >"$STORE/.meta/$TAG.json"
release "$TAG"
if [ "$RC" -ne 0 ] && says "$OUT" "carries extra.txt" && [ "$(uploads)" -eq 0 ]; then pass "R5 an extra asset is refused"; else fail "R5 rc=$RC: $OUT"; fi

jq --arg n "$LOCK" '.assets |= map(if .name == $n then .state = "starter" else . end)' "$TMP/full.json" >"$STORE/.meta/$TAG.json"
release "$TAG"
if [ "$RC" -ne 0 ] && says "$OUT" "$LOCK is attached but not uploaded" && [ "$(uploads)" -eq 0 ]; then
    pass "R6 an incomplete asset upload is refused by name"
else fail "R6 rc=$RC: $OUT"; fi
cp "$TMP/full.json" "$STORE/.meta/$TAG.json"

git -C "$BARE" tag 20260914-0200 "$COMMIT"
release 20260914-0200
if [ "$RC" -ne 0 ] && says "$OUT" "no published release 20260914-0200" && [ "$(uploads)" -eq 0 ] && nopool 20260914-0200; then pass "R7 a missing release is refused"; else fail "R7 rc=$RC: $OUT"; fi

cut 20260914-0300 "$COMMIT" true
release 20260914-0300
if [ "$RC" -ne 0 ] && says "$OUT" "is a draft" && [ "$(uploads)" -eq 0 ] && nopool 20260914-0300; then pass "R8 a draft release is refused"; else fail "R8 rc=$RC: $OUT"; fi

cut 20260914-0400 "$COMMIT" false other/mica-podman
release 20260914-0400
if [ "$RC" -ne 0 ] && says "$OUT" "is not the release 20260914-0400 of micaoss/mica-podman" && [ "$(uploads)" -eq 0 ]; then
    pass "R9 a release of another repository is refused"
else fail "R9 rc=$RC: $OUT"; fi

cut 20260914-0500 "$OTHER"
release 20260914-0500
if [ "$RC" -ne 0 ] && says "$OUT" "tag 20260914-0500 is $OTHER" && [ "$(uploads)" -eq 0 ] && nopool 20260914-0500; then pass "R10 a tag on another commit than HEAD is refused"; else fail "R10 rc=$RC: $OUT"; fi

cut 20260914-0600
git -C "$FIX" update-ref refs/remotes/origin/main "$OTHER"
release 20260914-0600
if [ "$RC" -ne 0 ] && says "$OUT" "not on origin/main" && ! says "$(calls)" "release"; then pass "R11 a commit not on main is refused"; else fail "R11 rc=$RC: $OUT"; fi
git -C "$FIX" update-ref refs/remotes/origin/main "$COMMIT"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" GH_TOKEN=fixture-token CORRUPT=1 MICA_RELEASE_DOWNLOAD="file://$STORE" MICA_RELEASE_GIT="$BARE" MICA_OCI_REGISTRY="$REG" \
    bash "$FIX/tools/release.sh" 20260914-0600 2>&1) || RC=$?
if [ "$RC" -ne 0 ] && says "$OUT" "downloads with other bytes"; then pass "R12 an anonymous download with other bytes is refused"; else fail "R12 rc=$RC: $OUT"; fi

cut 20260914-0650
release 20260914-0650
if [ "$RC" -ne 0 ] && says "$OUT" "the lock of mica-podman 20260914-0600 does not match its SHA256SUMS" && [ "$(uploads)" -eq 0 ] && nopool 20260914-0650; then
    pass "R12b a previous release whose lock does not match its SHA256SUMS is refused, not rebuilt around"
else fail "R12b rc=$RC: $OUT"; fi
rm -rf "$STORE/20260914-0600" "$STORE/.meta/20260914-0600.json" "$STORE/20260914-0650" "$STORE/.meta/20260914-0650.json"

for bad in 2026-09-14 20261399-2500 20260230-1200 ""; do
    release "$bad"
    if [ "$RC" -ne 0 ] && ! says "$(calls)" "release" && ! says "$(calls)" "api"; then pass "R13 the tag '$bad' is refused before any gh call"; else fail "R13 '$bad' rc=$RC: $OUT"; fi
done

cut 20260914-0700
refused() { # <label> <message>
    release 20260914-0700
    if [ "$RC" -ne 0 ] && says "$OUT" "$2" && [ -z "$(calls)" ] && nopool 20260914-0700; then pass "$1"; else fail "$1: rc=$RC: $OUT"; fi
    reset_debs
}
echo change >>"$FIX/.gitignore"
refused "R14 a dirty checkout is refused before any gh call" "uncommitted changes"
git -C "$FIX" checkout -q -- .gitignore
rm -rf "$FIX/_out/debs"; deb amd64 "5.8.6-9"; deb arm64
refused "R15 an archive of another version than the declared one is refused" "is not $V, the version deb/mica-podman.control declares"
rm -rf "$FIX/_out/debs/arm64"; deb arm64 "$V" "Mica-Source-Commit: $OTHER"
refused "R16 an archive naming a commit is refused" "carries Mica-Source-Commit"
deb amd64 "$V" "" payload extra_amd64.deb
refused "R17 an extra archive is refused" "holds 2 archives"
release 20260914-0700 ""
if [ "$RC" -ne 0 ] && says "$OUT" "GH_TOKEN must be set" && [ -z "$(calls)" ]; then pass "R18 no credential is refused by name"; else fail "R18 rc=$RC: $OUT"; fi

reset_debs
cut 20260914-0800
printf '{}' >"$TMP/empty" && printf 'other' >"$TMP/other"
for f in "$TMP/empty" "$TMP/other"; do
    d="sha256:$(sha256sum "$f" | awk '{print $1}')"
    loc=$(curl -sf -D - -o /dev/null -X POST "$REG/v2/$POOL/blobs/uploads/" | tr -d '\r' | sed -n 's/^[Ll]ocation: //p')
    case "$loc" in /*) loc="$REG$loc" ;; esac
    case "$loc" in *\?*) loc="$loc&digest=$d" ;; *) loc="$loc?digest=$d" ;; esac
    curl -sf -o /dev/null -X PUT -H 'Content-Type: application/octet-stream' --data-binary "@$f" "$loc"
done
jq -n --arg d "sha256:$(sha256sum "$TMP/other" | awk '{print $1}')" \
    '{schemaVersion: 2, mediaType: "application/vnd.oci.image.manifest.v1+json", artifactType: "application/vnd.mica.pool",
      config: {mediaType: "application/vnd.oci.empty.v1+json", digest: "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", size: 2},
      layers: [{mediaType: "application/vnd.mica.deb", digest: $d, size: 5}]}' >"$TMP/other.json"
curl -sf -o /dev/null -X PUT -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' --data-binary "@$TMP/other.json" "$REG/v2/$POOL/manifests/pool.arm64.20260914-0800"
release 20260914-0800
if [ "$RC" -ne 0 ] && says "$OUT" "pool.arm64.20260914-0800 already holds another manifest" && [ "$(uploads)" -eq 0 ] &&
    cmp -s <(manifest pool.arm64.20260914-0800) "$TMP/other.json"; then
    pass "R19 a pool tag holding another manifest is refused, not replaced, and no asset is uploaded"
else fail "R19 rc=$RC: $OUT"; fi

# ------------------------------------------------------------ the package-version guard across releases
# advance <message>: commit the fixture checkout's changes as main.
advance() {
    git -C "$FIX" -c user.name=f -c user.email=f@invalid commit -qam "$1"
    COMMIT=$(git -C "$FIX" rev-parse HEAD)
    git -C "$FIX" update-ref refs/remotes/origin/main "$COMMIT"
    git -C "$FIX" push -q -f "$BARE" HEAD:refs/heads/main
}
pooldigest() { manifest "pool.$1.$2" | sha256sum | awk '{print $1}'; } # <arch> <release>
lockpool() { awk -F'\t' -v a="$1" '$1 == "pool" && $2 == a { sub(/.*@sha256:/, "", $3); print $3 }' "$STORE/$2/$LOCK"; } # <arch> <release>
notag() { nopool "$1" && [ "$(uploads)" -eq 0 ]; }

reset_debs
cut 20260914-0900
release 20260914-0900
if [ "$RC" -eq 0 ] && says "$OUT" "mica-podman amd64 $V: reuse 20260914-0100" && says "$OUT" "mica-podman arm64 $V: reuse 20260914-0100" &&
    [ "$(pooldigest amd64 20260914-0900)" = "$(pooldigest amd64 $TAG)" ] && [ "$(lockpool arm64 20260914-0900)" = "$(lockpool arm64 $TAG)" ]; then
    pass "R20 the same version with the same inputs and bytes reuses the previous release: the pools keep their digests under the new tags"
else fail "R20 rc=$RC: $OUT"; fi

echo changed >"$FIX/overlay/fixture.conf"
advance "change an input"
cut 20260914-0910
release 20260914-0910
if [ "$RC" -ne 0 ] && says "$OUT" "inputs of mica-podman changed without a version bump" && notag 20260914-0910; then
    pass "R21 inputs changed without a version bump are refused before anything is written"
else fail "R21 rc=$RC: $OUT"; fi

echo fixture >"$FIX/overlay/fixture.conf"
advance "restore the input"
deb amd64 "$V" "" other
cut 20260914-0920
release 20260914-0920
if [ "$RC" -ne 0 ] && says "$OUT" "rebuilds to" && says "$OUT" "so bump the version" && notag 20260914-0920; then
    pass "R22 the same version and inputs with other bytes are refused"
else fail "R22 rc=$RC: $OUT"; fi
reset_debs

sed -i 's/^\(git\tpodman\t[^\t]*\t\)v5\.8\.6/\1v5.8.5/' "$FIX/locks/upstream.lock"
sed -i 's/^Version: .*/Version: 5.8.5-1/' "$FIX/deb/mica-podman.control"
advance "a lower version"
rm -rf "$FIX/_out/debs"; deb amd64 5.8.5-1; deb arm64 5.8.5-1
cut 20260914-0930
release 20260914-0930
if [ "$RC" -ne 0 ] && says "$OUT" "5.8.5-1 is lower than $V of mica-podman 20260914-0900" && notag 20260914-0930; then
    pass "R23 a version lower than the previous release's is refused"
else fail "R23 rc=$RC: $OUT"; fi

cp locks/upstream.lock "$FIX/locks/"
sed -i 's/^Version: .*/Version: 5.8.6-2/' "$FIX/deb/mica-podman.control"
advance "bump the revision"
V=5.8.6-2
rm -rf "$FIX/_out/debs"; deb amd64 "$V" "" bumped; deb arm64 "$V" "" bumped
cut 20260914-0940
release 20260914-0940
if [ "$RC" -eq 0 ] && says "$OUT" "mica-podman amd64 5.8.6-2: build" && [ "$(pooldigest amd64 20260914-0940)" != "$(pooldigest amd64 20260914-0900)" ] &&
    grep -c "^package	mica-podman	amd64	5.8.6-2	" "$STORE/20260914-0940/$LOCK" >/dev/null; then
    pass "R24 a higher version is built and published"
else fail "R24 rc=$RC: $OUT"; fi

# A previous release made before layers recorded mica.inputs: its pools without the annotation.
cut 20260914-0950
for a in amd64 arm64; do
    manifest "pool.$a.20260914-0940" | jq -c 'del(.layers[].annotations["mica.inputs"])' | tr -d '\n' >"$TMP/old-$a.json"
    curl -sf -o /dev/null -X PUT -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' --data-binary "@$TMP/old-$a.json" "$REG/v2/$POOL/manifests/pool.$a.20260914-0950"
done
{
    printf '# mica-lock v1\nrelease\tmica-podman\t20260914-0950\t%s\n' "$COMMIT"
    for a in amd64 arm64; do printf 'pool\t%s\tghcr.io/%s:pool.%s.20260914-0950@sha256:%s\n' "$a" "$POOL" "$a" "$(sha256sum "$TMP/old-$a.json" | awk '{print $1}')"; done
    grep '^package' "$STORE/20260914-0940/$LOCK"
} >"$STORE/20260914-0950/$LOCK"
(cd "$STORE/20260914-0950" && sha256sum "$LOCK" >SHA256SUMS)
jq --arg n "$LOCK" '.assets = [{name: $n, size: 1, digest: "sha256:00", state: "uploaded"}]' "$STORE/.meta/20260914-0950.json" >"$TMP/meta" && mv "$TMP/meta" "$STORE/.meta/20260914-0950.json"
cut 20260914-0955
release 20260914-0955
if [ "$RC" -eq 0 ] && says "$OUT" "mica-podman amd64 5.8.6-2: build" && [ "$(pooldigest arm64 20260914-0955)" = "$(pooldigest arm64 20260914-0940)" ]; then
    pass "R25 a previous release without recorded inputs is not a reuse source: everything is built"
else fail "R25 rc=$RC: $OUT"; fi

echo "RESULT: $FAIL_N failed, $PASS_N passed"
[ "$FAIL_N" -eq 0 ]
