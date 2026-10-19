#!/usr/bin/env bash
# mica-containerd against a real, rootful podman: a Compose project's service runs,
# is restarted when killed, stays stopped across a restart of the daemon, keeps
# running when the daemon stops, is recreated when its project changes, serves its
# log and its health, resolves another service by name on the project network, gets
# its named volume, and is removed with its project and network. A container run by
# hand under a service's name is never replaced, `compose check` refuses what the
# daemon refuses, a managed container no project declares is removed at start, and
# a service with its own container name runs on the host network with an added
# capability and a bind relative to its project directory, its name refused to another project and to a container run by hand. A service gets its
# devices, a device link resolved at each start, and one gone fails its start without
# harm to the daemon. Needs root, podman and the network (one image pull).
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
    "${MCD}" --socket "${SOCK}" --store "${TMP}/store" --log-dir "${TMP}/logs" --volumes "${TMP}/volumes" \
        --podman "${PODMAN}" --resync 3s --stop-timeout 2s >>"${TMP}/daemon.log" 2>&1 &
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
    "${PODMAN}" rm --force --ignore e2e2-app-1 e2e-stray e2e-hand >/dev/null 2>&1 || true
    "${PODMAN}" network ls --filter label=mica.containerd=1 --format '{{.Name}}' | xargs -r "${PODMAN}" network rm >/dev/null 2>&1 || true
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

write_project() { # <environment line>: the e2e project, web depending on a healthy db
    cat >"${TMP}/e2e.yaml" <<EOF
services:
  web:
    image: ${IMAGE}
    command: [sh, -c, "echo hello from web; exec sleep 3600"]
    restart: always
    environment: {$1}
    volumes: [data:/data]
    healthcheck: {test: [CMD, "true"], interval: 2s, timeout: 1s}
    depends_on: {db: {condition: service_healthy}}
  db:
    image: ${IMAGE}
    command: [sh, -c, "exec sleep 3600"]
    restart: unless-stopped
    healthcheck: {test: [CMD-SHELL, "true"], interval: 2s, timeout: 1s}
volumes:
  data: {}
EOF
}
write_project "E2E: '0'"
ctl up -p e2e -f "${TMP}/e2e.yaml" >/dev/null
if until_ok 30 phase_is e2e-web-1 running; then pass "E1 a project's service runs, after the service it depends on is healthy"; else fail "E1 phase $(field e2e-web-1 phase): $(ctl get e2e-web-1)"; fi

if until_ok 15 sh -c '[ "$('"${MCD}"' ctl --socket '"${SOCK}"' get e2e-web-1 | grep -c "\"health\": \"healthy\"")" = 1 ]'; then
    pass "E2 its health check runs and passes"
else fail "E2 $(ctl get e2e-web-1)"; fi

if until_ok 10 sh -c "ctl() { \"${MCD}\" ctl --socket \"${SOCK}\" \"\$@\"; }; ctl logs --tail 5 e2e-web-1 | grep -c 'hello from web' >/dev/null"; then
    pass "E3 its log is read through the API"
else fail "E3 $(ctl logs --tail 5 e2e-web-1 2>&1)"; fi
case "$("${PODMAN}" inspect --format '{{.HostConfig.LogConfig.Path}}' e2e-web-1)" in
"${TMP}/logs/e2e-web-1.log") pass "E3b its log file is under the log directory (/run on a device)" ;;
*) fail "E3b log path $("${PODMAN}" inspect --format '{{.HostConfig.LogConfig.Path}}' e2e-web-1)" ;;
esac

if [ "$("${PODMAN}" exec e2e-web-1 nslookup db 2>/dev/null | grep -c "^Address: $("${PODMAN}" inspect --format '{{(index .NetworkSettings.Networks "e2e_default").IPAddress}}' e2e-db-1)$")" -ge 1 ]; then
    pass "E4 a service resolves another by its name on the project network"
else fail "E4 $("${PODMAN}" exec e2e-web-1 nslookup db 2>&1) $("${PODMAN}" inspect --format '{{json .NetworkSettings.Networks}}' e2e-web-1)"; fi
if [ -d "${TMP}/volumes/e2e/data" ] && "${PODMAN}" exec e2e-web-1 sh -c 'echo x >/data/f' && [ -f "${TMP}/volumes/e2e/data/f" ]; then
    pass "E5 a named volume is the directory of its name under the project's volumes"
else fail "E5 $(ls -la "${TMP}/volumes" 2>&1)"; fi

"${PODMAN}" kill e2e-web-1 >/dev/null
if until_ok 30 sh -c '[ "$('"${MCD}"' ctl --socket '"${SOCK}"' get e2e-web-1 | sed -n "s/^  \"restarts\": \([0-9]*\),$/\1/p")" -ge 1 ]' &&
    until_ok 30 phase_is e2e-web-1 running; then
    pass "E6 a killed container is restarted"
else fail "E6 $(ctl get e2e-web-1)"; fi

before="$(cid e2e-web-1)"
stop_daemon
sleep 1
if [ "$("${PODMAN}" inspect --format '{{.State.Status}}' e2e-web-1)" = running ]; then
    pass "E7 stopping the daemon leaves its containers running"
else fail "E7 $("${PODMAN}" inspect --format '{{.State.Status}}' e2e-web-1)"; fi
start_daemon
if until_ok 10 phase_is e2e-web-1 running && [ "$(cid e2e-web-1)" = "${before}" ]; then
    pass "E8 a restarted daemon takes up the running container without recreating it"
else fail "E8 $(ctl get e2e-web-1)"; fi

ctl stop e2e-web-1 >/dev/null
until_ok 20 phase_is e2e-web-1 stopped || true
stop_daemon
start_daemon
sleep 4 # longer than a resync
if phase_is e2e-web-1 stopped && [ "$("${PODMAN}" inspect --format '{{.State.Status}}' e2e-web-1)" != running ]; then
    pass "E9 a stop survives a restart of the daemon"
else fail "E9 $(ctl get e2e-web-1)"; fi

ctl start e2e-web-1 >/dev/null
until_ok 30 phase_is e2e-web-1 running || true
before="$(cid e2e-web-1)"
write_project "E2E: '1'"
ctl up -p e2e -f "${TMP}/e2e.yaml" >/dev/null
if until_ok 30 sh -c '[ "$('"${PODMAN}"' inspect --format "{{.Id}}" e2e-web-1 2>/dev/null)" != "'"${before}"'" ]' &&
    until_ok 30 phase_is e2e-web-1 running &&
    "${PODMAN}" exec e2e-web-1 sh -c '[ "$E2E" = 1 ]'; then
    pass "E10 a changed service recreates its container"
else fail "E10 $(ctl get e2e-web-1)"; fi

"${PODMAN}" run --detach --name e2e2-app-1 "${IMAGE}" sleep 3600 >/dev/null
held="$(cid e2e2-app-1)"
printf 'services:\n  app: {image: %s, command: [sleep, "3600"]}\n' "${IMAGE}" >"${TMP}/e2e2.yaml"
if ! ctl up -p e2e2 -f "${TMP}/e2e2.yaml" >/dev/null 2>"${TMP}/e2e2.err" && grep -c "${held:0:12}" "${TMP}/e2e2.err" >/dev/null &&
    sleep 4 && [ "$(cid e2e2-app-1)" = "${held}" ] && ! ctl get e2e2-app-1 >/dev/null 2>&1; then
    pass "E11 a project naming a container run by hand is refused, and that container never replaced"
else fail "E11 $(cat "${TMP}/e2e2.err")"; fi

echo '{"projects": {}}' >"${TMP}/none.json"
ctl apply "${TMP}/none.json" >/dev/null
if until_ok 20 sh -c "! \"${PODMAN}\" container exists e2e-web-1 && ! \"${PODMAN}\" container exists e2e-db-1 && ! \"${PODMAN}\" network exists e2e_default" &&
    [ "$(cid e2e2-app-1)" = "${held}" ] && [ -d "${TMP}/volumes/e2e/data" ]; then
    pass "E12 an empty set removes every project's containers and network, never a container run by hand, never a volume"
else fail "E12 $("${PODMAN}" ps --all --format '{{.Names}}') $("${PODMAN}" network ls --format '{{.Name}}')"; fi

printf 'services:\n  app: {image: i, privileged: true}\n' >"${TMP}/bad.yaml"
set +e
"${MCD}" compose check "${TMP}/bad.yaml" >"${TMP}/check.out" 2>"${TMP}/check.err"
rc=$?
set -e
if [ "${rc}" = 1 ] && grep -q '"field": "privileged"' "${TMP}/check.err" && "${MCD}" compose check "${TMP}/e2e.yaml" >/dev/null &&
    printf 'services:\n  app: {image: i, volumes: [./data:/d]}\n' | "${MCD}" compose check --project e2e - | grep -c '"host": "/mica/volumes/e2e/data"' >/dev/null; then
    pass "E13 compose check refuses what the daemon refuses, with no daemon"
else fail "E13 rc ${rc}: $(cat "${TMP}/check.err")"; fi

# A container labelled managed that no project declares. A project runs beside it.
printf 'services:\n  web:\n    image: %s\n    command: [sleep, "3600"]\n' "${IMAGE}" >"${TMP}/cli.yaml"
ctl up -p cli -f "${TMP}/cli.yaml" >/dev/null
if until_ok 30 phase_is cli-web-1 running; then
    cli_id="$(cid cli-web-1)"
    stop_daemon
    "${PODMAN}" run --detach --name e2e-stray --label mica.containerd=1 "${IMAGE}" sleep 3600 >/dev/null
    start_daemon
    if until_ok 20 sh -c "! \"${PODMAN}\" container exists e2e-stray" &&
        [ "$(cid cli-web-1)" = "${cli_id}" ] && [ "$(cid e2e2-app-1)" = "${held}" ] && phase_is cli-web-1 running; then
        pass "E14 at start a managed container no project declares is removed; a project and a container run by hand stay"
    else fail "E14 $("${PODMAN}" ps --all --format '{{.Names}}') $(ls "${TMP}/store")"; fi
else fail "E14 $(ctl get cli-web-1 2>&1)"; fi

# A file written for docker compose, in its project directory: its own container
# name, the host network, an added capability, a relative bind.
mkdir -p "${TMP}/bit-56-core"
cat >"${TMP}/bit-56-core/compose.yaml" <<EOF
services:
  app: {image: ${IMAGE}, command: [sleep, "3600"], container_name: core, restart: unless-stopped,
        network_mode: host, cap_add: [NET_ADMIN], volumes: ["./data:/srv/lode"]}
EOF
cap_net_admin() { # CapEff of the container's first process has bit 12
    local eff
    eff="$("${PODMAN}" exec core sh -c 'sed -n "s/^CapEff:[[:space:]]*//p" /proc/1/status')"
    [ $(((16#${eff} >> 12) & 1)) = 1 ]
}
if (cd "${TMP}/bit-56-core" && ctl up >/dev/null) && until_ok 30 phase_is core running &&
    [ "$(field core project)" = bit-56-core ] &&
    [ "$("${PODMAN}" inspect --format '{{.HostConfig.NetworkMode}}' core)" = host ] && cap_net_admin &&
    "${PODMAN}" exec core sh -c 'echo x >/srv/lode/f' && [ -f "${TMP}/bit-56-core/data/f" ] && ! "${PODMAN}" network exists bit-56-core_default; then
    pass "E15 a service named core runs on the host network with NET_ADMIN, ./data in its project directory"
else fail "E15 $(ctl get core 2>&1) $("${PODMAN}" inspect --format '{{.HostConfig.NetworkMode}} {{.EffectiveCaps}}' core 2>&1)"; fi
printf 'services:\n  x: {image: %s, container_name: core}\n' "${IMAGE}" >"${TMP}/other.yaml"
"${PODMAN}" run --detach --name e2e-hand "${IMAGE}" sleep 3600 >/dev/null
hand="$(cid e2e-hand)"
printf 'services:\n  x: {image: %s, container_name: e2e-hand}\n' "${IMAGE}" >"${TMP}/byhand.yaml"
core="$(cid core)"
if ! ctl up -p other -f "${TMP}/other.yaml" >/dev/null 2>"${TMP}/other.err" && grep -c 'project bit-56-core' "${TMP}/other.err" >/dev/null &&
    ! ctl up -p byhand -f "${TMP}/byhand.yaml" >/dev/null 2>"${TMP}/byhand.err" && grep -c "${hand:0:12}" "${TMP}/byhand.err" >/dev/null &&
    sleep 4 && [ "$(cid core)" = "${core}" ] && [ "$(cid e2e-hand)" = "${hand}" ]; then
    pass "E16 a container name is refused to another project and to a container run by hand"
else fail "E16 $(cat "${TMP}/other.err" "${TMP}/byhand.err")"; fi

# Devices: a node, and a link to one as /dev/serial/by-id/... is, resolved at start.
ln -s /dev/null "${TMP}/by-id"
printf 'services:\n  app: {image: %s, command: [sleep, "3600"], restart: always, devices: ["/dev/null:/dev/x", "%s:/dev/y:rw"]}\n' \
    "${IMAGE}" "${TMP}/by-id" >"${TMP}/dev.yaml"
if ctl up -p dev -f "${TMP}/dev.yaml" >/dev/null && until_ok 30 phase_is dev-app-1 running &&
    "${PODMAN}" exec dev-app-1 sh -c 'test -c /dev/x && test -c /dev/y && echo x >/dev/y'; then
    pass "E17 a service's devices are nodes inside it, a link given as the node it names"
else fail "E17 $(ctl get dev-app-1 2>&1)"; fi
printf 'services:\n  app: {image: %s, devices: [/dev/mica-e2e-absent]}\n' "${IMAGE}" >"${TMP}/absent.yaml"
if ! ctl up -p absent -f "${TMP}/absent.yaml" >/dev/null 2>"${TMP}/absent.err" && grep -c 'device /dev/mica-e2e-absent is absent' "${TMP}/absent.err" >/dev/null; then
    pass "E18 a device absent at declaration is refused by its path"
else fail "E18 $(cat "${TMP}/absent.err")"; fi
rm "${TMP}/by-id" # unplugged
"${PODMAN}" kill dev-app-1 >/dev/null
if until_ok 30 sh -c "\"${MCD}\" ctl --socket \"${SOCK}\" get dev-app-1 | grep -c 'by-id is absent' >/dev/null" && ctl status >/dev/null; then
    ln -s /dev/null "${TMP}/by-id" # plugged in again
    ctl start dev-app-1 >/dev/null
    if until_ok 30 phase_is dev-app-1 running; then
        pass "E19 a device gone fails the start with its path, the daemon unharmed; back, the service runs"
    else fail "E19 $(ctl get dev-app-1)"; fi
else fail "E19 $(ctl get dev-app-1)"; fi

ctl down -p dev >/dev/null
(cd "${TMP}/bit-56-core" && ctl down >/dev/null)
if until_ok 20 sh -c "! \"${PODMAN}\" container exists dev-app-1 && ! \"${PODMAN}\" container exists core" && [ -f "${TMP}/bit-56-core/data/f" ]; then
    pass "E20 down removes a project, named by -p or by its directory, never its data"
else fail "E20 $("${PODMAN}" ps --all --format '{{.Names}}')"; fi

echo "RESULT: ${FAIL_N} failed, ${PASS_N} passed"
[ "${FAIL_N}" -eq 0 ] || { echo "--- daemon log"; tail -40 "${TMP}/daemon.log"; exit 1; }
