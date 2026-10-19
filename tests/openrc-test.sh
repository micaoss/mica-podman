#!/usr/bin/env bash
# deb/mica-containerd.openrc, sourced as openrc-run would, with openrc-run's helpers
# stubbed and a podman that records its calls: a plain stop of the service leaves the
# containers running (a restarted daemon adopts them), and a stop while the system
# goes down stops them after the supervisor, so nothing holds DATA at poweroff.
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

echo "RESULT: $FAIL_N failed, $PASS_N passed"
[ "$FAIL_N" -eq 0 ]
