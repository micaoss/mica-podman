#!/usr/bin/env bash
# The archive of _out/debs/<arch>/pool installed and run: a privileged container of
# the mica-build-env base image, the archive's Debian Depends from the apt row of
# locks/mica-system-base.lock (the snapshot the device roots install from), the
# archive with dpkg (mica-system is not installed, so the /mica/containers
# directories it would mount are made, on a volume of their own). Then podman runs
# containers as mica-containerd runs them: exit codes, a project network with name
# resolution, a published port, a k8s-file log, a health check, capabilities and a
# device. Needs docker and the network (one image pull).
#
#   bash tests/podman-smoke.sh [--arch <amd64|arm64>]   (default: the host's)
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD
TOOLS="${REPO_ROOT}/bin/mica-tools"
die() { echo "podman-smoke: error: $*" >&2; exit 2; }

ARCH="$(dpkg --print-architecture 2>/dev/null || { [ "$(uname -m)" = aarch64 ] && echo arm64 || echo amd64; })"
while [ "$#" -gt 0 ]; do
    case "$1" in
    --arch) ARCH="${2-}"; shift 2 ;;
    *) echo "usage: bash tests/podman-smoke.sh [--arch <amd64|arm64>]" >&2; exit 2 ;;
    esac
done
case "${ARCH}" in amd64 | arm64) ;; *) die "--arch must be amd64 or arm64" ;; esac
mapfile -t debs < <(find "_out/debs/${ARCH}/pool" -maxdepth 1 -type f -name 'mica-podman_*.deb' 2>/dev/null)
[ "${#debs[@]}" -eq 1 ] || die "_out/debs/${ARCH}/pool holds ${#debs[@]} mica-podman archives, not one; run MICA_ARCH=${ARCH} make podman deb"
DEB="${REPO_ROOT}/${debs[0]}"

apt="$("${TOOLS}" lock rows locks/mica-system-base.lock apt)"
[ "$(grep -c . <<<"${apt}")" -eq 1 ] || die "locks/mica-system-base.lock does not carry exactly one apt row"
IFS=$'\t' read -r _ uri suites components keyring <<<"${apt}"
SOURCES="Types: deb
URIs: ${uri}
Suites: ${suites}
Components: ${components}
Signed-By: ${keyring}
Check-Valid-Until: no"

vol="mica-podman-smoke-$$"
docker volume create --label ai-agent=true "${vol}" >/dev/null
trap 'docker volume rm "${vol}" >/dev/null 2>&1 || true' EXIT
docker run --rm --label ai-agent=true --privileged --platform "linux/${ARCH}" \
    -v "${REPO_ROOT}:${REPO_ROOT}:ro" -v "${vol}:/mica" \
    -e SOURCES="${SOURCES}" -e DEB="${DEB}" -e IMAGE="${SMOKE_IMAGE:-docker.io/library/busybox:1.37}" \
    "$("${TOOLS}" from --ref mica-build-env:base)" bash -euc '
    printf "%s\n" "${SOURCES}" >/etc/apt/sources.list.d/mica-system-base.sources
    apt-get update -qq >/dev/null
    depends="$(dpkg-deb --field "${DEB}" Depends | tr "," "\n" | sed "s/(.*//; s/[[:space:]]//g" | grep -v "^mica-" | tr "\n" " ")"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends ca-certificates ${depends} >/dev/null
    dpkg --force-depends --install "${DEB}" >/dev/null 2>&1
    install -d /mica/containers/tmp /mica/containers/networks
    # Nested in docker, this container is the root of its cgroup namespace and holds
    # processes, so no controller can be delegated below it: move them to a leaf
    # first, as docker:dind does. A device root has its own cgroup tree.
    if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
        mkdir -p /sys/fs/cgroup/init
        xargs -rn1 </sys/fs/cgroup/cgroup.procs >/sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
        sed "s/[^ ]* */+&/g" /sys/fs/cgroup/cgroup.controllers >/sys/fs/cgroup/cgroup.subtree_control
    fi
    PASS_N=0 FAIL_N=0
    check() { local label="$1"; shift; if "$@" >/tmp/out 2>&1; then PASS_N=$((PASS_N + 1)); echo "PASS: ${label}"; else FAIL_N=$((FAIL_N + 1)); echo "FAIL: ${label}"; tail -5 /tmp/out; fi; }
    echo "podman-smoke: $(dpkg-deb --field "${DEB}" Package Version | tr "\n" " ")on $(dpkg --print-architecture)"
    check "S1 crun, netavark and overlay under /mica/containers" sh -c "podman info --format \"{{.Host.OCIRuntime.Name}} {{.Host.NetworkBackend}} {{.Store.GraphDriverName}} {{.Store.GraphRoot}}\" | grep -x \"crun netavark overlay /mica/containers/storage\""
    check "S2 an image is pulled" podman pull -q "${IMAGE}"
    check "S3 a container runs and its exit code is reported" sh -c "podman run --rm \"${IMAGE}\" true && { podman run --rm \"${IMAGE}\" sh -c \"exit 3\"; [ \$? -eq 3 ]; }"
    podman network create smoke >/dev/null
    podman run -d --name smoke-a --network smoke --network-alias api "${IMAGE}" sleep 600 >/dev/null
    # In the network domain dns.podman: a bare name would also be tried under the
    # search domains of the host, which the resolver of a CI runner answers with NXDOMAIN.
    check "S4 a container name and an alias resolve on a network (aardvark-dns)" podman run --rm --network smoke "${IMAGE}" sh -c "nslookup smoke-a.dns.podman && nslookup api.dns.podman"
    check "S5 netavark wrote its nftables rules" sh -c "nft list tables | grep -c netavark"
    podman run -d --name smoke-web -p 18080:80 "${IMAGE}" sh -c "echo ok >/tmp/index.html && exec httpd -f -p 80 -h /tmp" >/dev/null
    sleep 1
    check "S6 a published port answers on the host" sh -c "podman run --rm --network host \"${IMAGE}\" wget -qO- http://127.0.0.1:18080/index.html | grep -x ok"
    podman run --name smoke-log --log-driver k8s-file --log-opt path=/run/smoke-log.log "${IMAGE}" echo logged >/dev/null
    check "S7 a k8s-file log is written and read back" sh -c "grep -c logged /run/smoke-log.log && podman logs smoke-log | grep -x logged"
    podman run -d --name smoke-health --health-cmd "test -d /" --health-interval 0 "${IMAGE}" sleep 600 >/dev/null
    check "S8 a health check runs" podman healthcheck run smoke-health
    check "S9 cap_add and cap_drop reach the container" sh -c "[ \"\$(podman run --rm --cap-drop ALL \"${IMAGE}\" sed -n \"s/^CapEff:[[:space:]]*//p\" /proc/self/status)\" = 0000000000000000 ] && podman run --rm --cap-drop ALL --cap-add NET_ADMIN \"${IMAGE}\" sed -n \"s/^CapEff:[[:space:]]*//p\" /proc/self/status | grep -x 0000000000001000"
    check "S10 a device is passed in" podman run --rm --device /dev/null:/dev/smoke "${IMAGE}" test -c /dev/smoke
    podman rm -f -t 0 smoke-a smoke-web smoke-log smoke-health >/dev/null
    podman network rm smoke >/dev/null
    echo "RESULT: ${FAIL_N} failed, ${PASS_N} passed"
    [ "${FAIL_N}" -eq 0 ]'
