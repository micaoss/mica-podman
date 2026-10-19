#!/usr/bin/env bash
# The gates of mica-containerd/: gofmt, go vet and the tests under the race detector,
# in the mica-build-env go image by digest, with no module download (the
# modules are vendored). The build cache is the caller's MICA_GO_CACHE, else _out/cache/go-build.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="$("${REPO_ROOT}/bin/mica-tools" from --ref mica-build-env:go)"
CACHE="${MICA_GO_CACHE:-${REPO_ROOT}/_out/cache/go-build}"
mkdir -p "${CACHE}"
docker run --rm --label ai-agent=true --network none --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "${REPO_ROOT}:${REPO_ROOT}" -w "${REPO_ROOT}/mica-containerd" \
    -v "${CACHE}:/cache/go-build" -e GOCACHE=/cache/go-build -e GOTOOLCHAIN=local -e GOFLAGS=-mod=vendor -e GOPROXY=off -e CCACHE_DISABLE=1 \
    "${IMAGE}" sh -euc '
        unformatted="$(gofmt -l cmd internal)"
        [ -z "${unformatted}" ] || { echo "containerd-test: not gofmt-formatted:"; echo "${unformatted}"; exit 1; } >&2
        go vet ./...
        go test -race -count=1 ./...'
