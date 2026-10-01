#!/usr/bin/env bash
# Build mica-containerd as a static binary (CGO off, trimmed, stripped) for one
# architecture, in the mica-build-env go image by digest, with no module download.
#
#   bash tools/containerd-build.sh <out> [amd64|arm64]   (default: the host's)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { echo "usage: bash tools/containerd-build.sh <out> [amd64|arm64]" >&2; exit 2; }
OUT="$(realpath -m "$1")"
ARCH="${2:-$(dpkg --print-architecture 2>/dev/null || { [ "$(uname -m)" = aarch64 ] && echo arm64 || echo amd64; })}"
case "${ARCH}" in amd64 | arm64) ;; *) echo "containerd-build.sh: --arch must be amd64 or arm64" >&2; exit 2 ;; esac
case "${OUT}/" in "${REPO_ROOT}/"*) ;; *) echo "containerd-build.sh: ${OUT} is outside the repository the build container mounts" >&2; exit 2 ;; esac
VERSION="$(sed -n 's/^Version: //p' "${REPO_ROOT}/deb/mica-podman.control")"
IMAGE="$("${REPO_ROOT}/bin/mica-tools" from --ref mica-build-env:go)"
CACHE="${MICA_GO_CACHE:-${REPO_ROOT}/_out/cache/go-build}"
mkdir -p "${CACHE}" "$(dirname "${OUT}")"
docker run --rm --label ai-agent=true --network none --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "${REPO_ROOT}:${REPO_ROOT}" -w "${REPO_ROOT}/mica-containerd" \
    -v "${CACHE}:/cache/go-build" -e GOCACHE=/cache/go-build -e GOTOOLCHAIN=local -e GOFLAGS=-mod=mod -e GOPROXY=off -e CCACHE_DISABLE=1 \
    -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH="${ARCH}" \
    "${IMAGE}" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${OUT}" ./cmd/mica-containerd
echo "containerd-build.sh: ${OUT#"${REPO_ROOT}"/} (${ARCH}, ${VERSION})"
