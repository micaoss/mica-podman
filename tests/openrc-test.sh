#!/usr/bin/env bash
# deb/mica-containerd.openrc, sourced as openrc-run would, with openrc-run's helpers
# stubbed and a podman that records its calls: a plain stop of the service leaves the
# containers running (a restarted daemon adopts them), and a stop while the system
# goes down stops them after the supervisor, so nothing holds DATA at poweroff.
# The start checks the engine root with mica-require-mount (a stub here) and is
# refused when it says so; the service needs mica-fstab.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
mkdir -p _out
TMP=$(mktemp -d "$REPO_ROOT/_out/openrc-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }

mkdir -p "$TMP/bin"
printf '#!/bin/sh\necho "podman $*" >>"%s/calls"\n' "$TMP" >"$TMP/bin/podman"
chmod +x "$TMP/bin/podman"
# stop_hook <RC_GOINGDOWN>: the service's stop_post, as openrc-run runs it.
stop_hook() {
    rm -f "$TMP/calls"
    PATH="$TMP/bin:$PATH" RC_GOINGDOWN="$1" sh -c '
        yesno() { case "$1" in [Yy][Ee][Ss] | [Tt][Rr][Uu][Ee] | [Oo][Nn] | 1) return 0 ;; esac; return 1; }
        ebegin() { :; }
        eend() { return "${1:-0}"; }
        . "$0"
        if command -v stop_post >/dev/null 2>&1; then stop_post; fi' "$REPO_ROOT/deb/mica-containerd.openrc"
}

stop_hook NO
if [ ! -e "$TMP/calls" ]; then pass "O1 a plain stop leaves the containers running"; else fail "O1 $(cat "$TMP/calls")"; fi

stop_hook YES
if grep -c "^podman stop --all " "$TMP/calls" >/dev/null 2>&1; then
    pass "O2 a stop while the system goes down stops every container"
else fail "O2 calls: $(cat "$TMP/calls" 2>/dev/null)"; fi

# start_hook <helper exit code>: the service's start_pre, with mica-require-mount
# replaced by a stub that records its arguments, writes to stderr and exits so.
start_hook() {
    rm -f "$TMP/calls"
    printf '#!/bin/sh\necho "require $*" >>"%s/calls"\necho "stub: not mounted" >&2\nexit %s\n' "$TMP" "$1" >"$TMP/bin/require-mount"
    chmod +x "$TMP/bin/require-mount"
    RC=0
    OUT=$(MICA_REQUIRE_MOUNT="$TMP/bin/require-mount" sh -c '
        . "$0"
        start_pre' "$REPO_ROOT/deb/mica-containerd.openrc" 2>&1) || RC=$?
}

start_hook 0
if [ "$RC" -eq 0 ] && [ "$(cat "$TMP/calls")" = "require /mica/containers/storage" ]; then
    pass "O3 the start checks the engine root /mica/containers/storage and goes on when it may"
else fail "O3 rc=$RC calls: $(cat "$TMP/calls" 2>/dev/null)"; fi

start_hook 1
if [ "$RC" -ne 0 ] && [ "$OUT" = "stub: not mounted" ]; then
    pass "O4 a user's engine root that is not mounted refuses the start, and the reason reaches the log"
else fail "O4 rc=$RC: $OUT"; fi

deps=$(sh -c '
    need() { echo "need $*"; }
    use() { echo "use $*"; }
    . "$0"
    depend' "$REPO_ROOT/deb/mica-containerd.openrc")
if printf '%s\n' "$deps" | grep -Ec '^need( [^ ]+)* mica-fstab( |$)' >/dev/null; then
    pass "O5 the service needs mica-fstab, so the user's mounts come first"
else fail "O5 depend: $deps"; fi

echo "RESULT: $FAIL_N failed, $PASS_N passed"
[ "$FAIL_N" -eq 0 ]
