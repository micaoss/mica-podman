#!/usr/bin/env bash
# tools/package-inputs.sh in a fixture checkout: the hash covers what decides
# the archive's bytes and the architecture, and nothing else. Offline.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
mkdir -p _out
TMP=$(mktemp -d "$REPO_ROOT/_out/package-inputs-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }

FIX="$TMP/repo"
mkdir -p "$FIX"
git ls-files -z | xargs -0 -I{} cp --parents {} "$FIX/"
git -C "$FIX" init -q
git -C "$FIX" -c user.name=f -c user.email=f@invalid add -A
git -C "$FIX" -c user.name=f -c user.email=f@invalid commit -qm fixture
h() { bash "$FIX/tools/package-inputs.sh" "$@"; }

A=$(h amd64)
if [[ "$A" =~ ^[0-9a-f]{64}$ ]] && [ "$(h amd64)" = "$A" ]; then pass "P1 the hash is a stable sha256"; else fail "P1 got '$A'"; fi
if [ "$(h arm64)" != "$A" ]; then pass "P2 the architecture is an input"; else fail "P2 amd64 and arm64 hash alike"; fi

changed() { # <label> <file> -> the hash moves when <file> changes
    cp "$FIX/$2" "$TMP/bak"
    echo "# changed" >>"$FIX/$2"
    if [ "$(h amd64)" != "$A" ]; then pass "$1"; else fail "$1: $2 is not covered"; fi
    cp "$TMP/bak" "$FIX/$2"
}
same() { # <label> <file> -> the hash stays when <file> changes
    cp "$FIX/$2" "$TMP/bak"
    echo "# changed" >>"$FIX/$2"
    if [ "$(h amd64)" = "$A" ]; then pass "$1"; else fail "$1: $2 moved the hash"; fi
    cp "$TMP/bak" "$FIX/$2"
}
changed "P3 the declared version and epoch (deb/mica-podman.control) are inputs" deb/mica-podman.control
changed "P4 the upstream pins are inputs" locks/upstream.lock
changed "P5 the engine Dockerfile is an input" Dockerfile
changed "P6 the packer is an input" deb/pack.sh
changed "P7 the shipped configuration is an input" overlay/etc/containers/storage.conf
changed "P8 the build and pack scripts are inputs" tools/package.sh
same "P9 the build-env lock is not an input (the byte comparison catches a toolchain move)" locks/mica-build-env.lock
same "P10 the Base lock is not an input" locks/mica-system-base.lock
same "P11 documentation is not an input" README.md
same "P12 the release publisher is not an input" tools/release.sh

if bash "$FIX/tools/package-inputs.sh" --manifest amd64 | grep -c '^arch amd64$' >/dev/null; then pass "P13 --manifest lists what the hash is taken over"; else fail "P13"; fi

echo "RESULT: $FAIL_N failed, $PASS_N passed"
[ "$FAIL_N" -eq 0 ]
