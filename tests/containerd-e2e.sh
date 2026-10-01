#!/usr/bin/env bash
# mica-containerd against a real, rootful podman: a declared container runs, is
# restarted when killed, stays stopped across a restart of the daemon, keeps running
# when the daemon stops, is recreated when its declaration changes, serves its log
# and its health, and is removed with its declaration. Needs root, podman and the
# network (one image pull).
#
#   MCD=<mica-containerd binary> bash tests/containerd-e2e.sh
set -euo pipefail

[ "$(id -u)" = 0 ] || { echo "containerd-e2e: needs root (rootful podman)" >&2; exit 2; }
PODMAN="$(command -v podman)" || { echo "containerd-e2e: podman is not on PATH" >&2; exit 2; }
MCD="${MCD:?MCD must name the mica-containerd binary}"
IMAGE="${E2E_IMAGE:-docker.io/library/busybox:1.37}"

TMP="$(mktemp -d /tmp/mcd-e2e.XXXXXX)"
SOCK="${TMP}/api.sock"
PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }
ctl() { "${MCD}" ctl --socket "${SOCK}" "$@"; }
field() { # <container> <jq-free field>: one scalar of `ctl get`
    ctl get "$1" | sed -n "s/^  \"$2\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}$/\1/p"
}
cid() { "${PODMAN}" inspect --format '{{.Id}}' "$1" 2>/dev/null || true; }

DAEMON=""
start_daemon() {
    "${MCD}" --socket "${SOCK}" --store "${TMP}/store" --log-dir "${TMP}/logs" --podman "${PODMAN}" \
        --resync 3s --stop-timeout 2s >>"${TMP}/daemon.log" 2>&1 &
    DAEMON=$!
    for _ in $(seq 1 50); do [ -S "${SOCK}" ] && ctl status >/dev/null 2>&1 && return 0; sleep 0.1; done
    echo "containerd-e2e: the daemon did not come up" >&2
    cat "${TMP}/daemon.log" >&2
    exit 1
}
stop_daemon() {
    [ -z "${DAEMON}" ] && return 0
    kill -TERM "${DAEMON}" 2>/dev/null || true
    wait "${DAEMON}" 2>/dev/null || true
    DAEMON=""
}
cleanup() {
    stop_daemon
    "${PODMAN}" ps --all --filter label=mica.containerd=1 --format '{{.Names}}' | xargs -r "${PODMAN}" rm --force >/dev/null 2>&1 || true
    rm -rf "${TMP}"
}
trap cleanup EXIT

# until <seconds> <command...>: poll until the command succeeds.
until_ok() {
    local limit=$(($1 * 10))
    shift
    for _ in $(seq 1 "${limit}"); do "$@" >/dev/null 2>&1 && return 0; sleep 0.1; done
    return 1
}
phase_is() { [ "$(field "$1" phase)" = "$2" ]; }

"${PODMAN}" pull --quiet "${IMAGE}" >/dev/null
start_daemon

cat >"${TMP}/web.json" <<EOF
{"image": "${IMAGE}", "command": ["sh", "-c", "echo hello from web; exec sleep 3600"],
 "restart": {"policy": "always"}, "autostart": true,
 "health": {"command": ["true"], "interval_seconds": 2, "timeout_seconds": 1}}
EOF
ctl put e2e-web "${TMP}/web.json" >/dev/null
if until_ok 30 phase_is e2e-web running; then pass "E1 a declared container runs"; else fail "E1 phase $(field e2e-web phase): $(ctl get e2e-web)"; fi

if until_ok 15 sh -c '[ "$('"${MCD}"' ctl --socket '"${SOCK}"' get e2e-web | grep -c "\"health\": \"healthy\"")" = 1 ]'; then
    pass "E2 its health check runs and passes"
else fail "E2 $(ctl get e2e-web)"; fi

if until_ok 10 sh -c "ctl() { \"${MCD}\" ctl --socket \"${SOCK}\" \"\$@\"; }; ctl logs --tail 5 e2e-web | grep -c 'hello from web' >/dev/null"; then
    pass "E3 its log is read through the API"
else fail "E3 $(ctl logs --tail 5 e2e-web 2>&1)"; fi
case "$("${PODMAN}" inspect --format '{{.HostConfig.LogConfig.Path}}' e2e-web)" in
"${TMP}/logs/e2e-web.log") pass "E3b its log file is under the log directory (/run on a device)" ;;
*) fail "E3b log path $("${PODMAN}" inspect --format '{{.HostConfig.LogConfig.Path}}' e2e-web)" ;;
esac

"${PODMAN}" kill e2e-web >/dev/null
if until_ok 30 sh -c '[ "$('"${MCD}"' ctl --socket '"${SOCK}"' get e2e-web | sed -n "s/^  \"restarts\": \([0-9]*\),$/\1/p")" -ge 1 ]' &&
    until_ok 30 phase_is e2e-web running; then
    pass "E4 a killed container is restarted"
else fail "E4 $(ctl get e2e-web)"; fi

before="$(cid e2e-web)"
stop_daemon
sleep 1
if [ "$("${PODMAN}" inspect --format '{{.State.Status}}' e2e-web)" = running ]; then
    pass "E5 stopping the daemon leaves its containers running"
else fail "E5 $("${PODMAN}" inspect --format '{{.State.Status}}' e2e-web)"; fi
start_daemon
if until_ok 10 phase_is e2e-web running && [ "$(cid e2e-web)" = "${before}" ]; then
    pass "E6 a restarted daemon adopts the running container without recreating it"
else fail "E6 $(ctl get e2e-web)"; fi

ctl stop e2e-web >/dev/null
until_ok 20 phase_is e2e-web stopped || true
stop_daemon
start_daemon
sleep 4 # longer than a resync
if phase_is e2e-web stopped && [ "$("${PODMAN}" inspect --format '{{.State.Status}}' e2e-web)" != running ]; then
    pass "E7 a stop survives a restart of the daemon"
else fail "E7 $(ctl get e2e-web)"; fi

ctl start e2e-web >/dev/null
until_ok 30 phase_is e2e-web running || true
before="$(cid e2e-web)"
sed 's/"autostart": true/"autostart": true, "environment": {"E2E": "1"}/' "${TMP}/web.json" >"${TMP}/web2.json"
ctl put e2e-web "${TMP}/web2.json" >/dev/null
if until_ok 30 sh -c '[ "$('"${PODMAN}"' inspect --format "{{.Id}}" e2e-web 2>/dev/null)" != "'"${before}"'" ]' &&
    until_ok 30 phase_is e2e-web running &&
    "${PODMAN}" exec e2e-web sh -c '[ "$E2E" = 1 ]'; then
    pass "E8 a changed declaration recreates the container"
else fail "E8 $(ctl get e2e-web)"; fi

ctl delete e2e-web >/dev/null
if until_ok 20 sh -c "! \"${PODMAN}\" container exists e2e-web"; then
    pass "E9 a deleted declaration removes its container"
else fail "E9 e2e-web still exists"; fi

echo "RESULT: ${FAIL_N} failed, ${PASS_N} passed"
[ "${FAIL_N}" -eq 0 ] || { echo "--- daemon log"; tail -40 "${TMP}/daemon.log"; exit 1; }
