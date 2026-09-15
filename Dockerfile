# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

# The container engine from pinned upstream source: src fetches and verifies,
# c/rust/go stages compile for the target, verify asserts, artifact exports.
# Separate builder stages keep one component's -dev list out of the others'
# cache keys. The bases are the mica-build-env images of
# locks/mica-build-env.lock (build.sh passes them; no defaults).
ARG MICA_BUILD_BASE
ARG MICA_BUILD_C
ARG MICA_BUILD_GO
ARG MICA_BUILD_RUST

FROM --platform=$BUILDPLATFORM ${MICA_BUILD_BASE} AS src
COPY locks/upstream.lock /upstream.lock

# Shallow clone of each git row of locks/upstream.lock at its tag, with
# submodules (crun needs libocispec), verified against the pinned commit.
RUN --mount=type=cache,target=/root/.cache/git \
    set -eu; \
    fetch() { \
        name="$1"; \
        row="$(awk -F'\t' -v n="${name}" '$1 == "git" && $2 == n' /upstream.lock)"; \
        [ -n "${row}" ] || { echo "error: locks/upstream.lock pins no ${name}" >&2; exit 1; }; \
        url="$(printf '%s\n' "${row}" | cut -f3)"; tag="$(printf '%s\n' "${row}" | cut -f4)"; want="$(printf '%s\n' "${row}" | cut -f5)"; \
        git clone --quiet --depth 1 --branch "${tag}" \
            --recurse-submodules --shallow-submodules "${url}" "/src/${name}"; \
        got="$(git -C "/src/${name}" rev-parse HEAD)"; \
        if [ "${want}" != "${got}" ]; then \
            echo "error: ${name} ${tag} is commit ${got}, but locks/upstream.lock pins ${want}. Either the tag was moved upstream or the pin is stale; do not paste the new commit in without finding out which" >&2; exit 1; \
        fi; \
        echo "ok ${name} ${tag} ${got}"; \
    }; \
    fetch podman; fetch crun; fetch conmon; fetch netavark; fetch aardvark-dns; fetch catatonit

FROM ${MICA_BUILD_C} AS c-build
RUN --mount=type=cache,target=/var/cache/apt,id=apt-cache-c,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,id=apt-lists-c,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    apt-get update && apt-get install -y --no-install-recommends \
        libseccomp-dev libcap-dev \
        libjson-c-dev libyajl-dev \
        libglib2.0-dev \
        libsystemd-dev
COPY --from=src /src/crun /src/crun
COPY --from=src /src/conmon /src/conmon
COPY --from=src /src/catatonit /src/catatonit
RUN mkdir -p /out

# crun and conmon must link libsystemd: configure drops systemd cgroup and
# journald support silently, and both fail only on a booted device.
RUN --mount=type=cache,target=/ccache,id=ccache-c \
    set -eu; cd /src/crun; \
    ./autogen.sh; \
    ./configure; \
    make -j"$(nproc)"; \
    install -m0755 crun /out/crun; \
    ldd /out/crun | grep -q libsystemd || \
        { echo "error: crun linked no libsystemd. configure probes it with pkg-config and disables systemd support silently when it is missing; this binary would fail every podman run on the device with 'systemd not supported'" >&2; exit 1; }

RUN --mount=type=cache,target=/ccache,id=ccache-c \
    set -eu; cd /src/conmon; \
    make -j"$(nproc)" bin/conmon; \
    install -m0755 bin/conmon /out/conmon; \
    ldd /out/conmon | grep -q libsystemd || \
        { echo "error: conmon was built WITHOUT journald support. Its Makefile compiles the journald path out when libsystemd is not found, silently -- and containers.conf sets log_driver=journald, so every container start fails with 'Include journald in compilation path'" >&2; exit 1; }; \
    echo "conmon: linked against libsystemd, so log_driver=journald works"

# catatonit is static: it runs inside containers with their own libc.
RUN --mount=type=cache,target=/ccache,id=ccache-c \
    set -eu; cd /src/catatonit; \
    ./autogen.sh && ./configure LDFLAGS="-static"; \
    make -j"$(nproc)"; \
    install -m0755 catatonit /out/catatonit

FROM ${MICA_BUILD_RUST} AS rust-build
RUN --mount=type=cache,target=/var/cache/apt,id=apt-cache-rust,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,id=apt-lists-rust,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    apt-get update && apt-get install -y --no-install-recommends \
        protobuf-compiler pkgconf
COPY --from=src /src/netavark /src/netavark
COPY --from=src /src/aardvark-dns /src/aardvark-dns

# Cached target/ directories; cargo rebuilds by fingerprint. The embedded build
# time is SOURCE_DATE_EPOCH, each component's pinned commit time (the tree hash
# covers it: `git archive` stamps it on every entry), so rebuilds are identical.
# Each crate's own build-script output and fingerprints are dropped first: its
# build.rs reruns only when build.rs changes, so a cached run would keep the
# build time of whichever build filled the cache.
RUN --mount=type=cache,target=/usr/local/cargo/registry,id=cargo-registry \
    --mount=type=cache,target=/usr/local/cargo/git,id=cargo-git \
    --mount=type=cache,target=/src/netavark/target,id=netavark-target \
    --mount=type=cache,target=/src/aardvark-dns/target,id=aardvark-target \
    set -eu; mkdir -p /out; \
    rm -rf /src/netavark/target/release/build/netavark-* /src/netavark/target/release/.fingerprint/netavark-* \
        /src/aardvark-dns/target/release/build/aardvark-dns-* /src/aardvark-dns/target/release/.fingerprint/aardvark-dns-*; \
    cd /src/netavark && SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" cargo build --release; \
    install -m0755 target/release/netavark /out/netavark; \
    cd /src/aardvark-dns && SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" cargo build --release; \
    install -m0755 target/release/aardvark-dns /out/aardvark-dns

FROM ${MICA_BUILD_GO} AS go-build
RUN --mount=type=cache,target=/var/cache/apt,id=apt-cache-go,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,id=apt-lists-go,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkgconf \
        libseccomp-dev libsubid-dev libsqlite3-dev libsystemd-dev
COPY --from=src /src/podman /src/podman

# openpgp avoids gpgme and GnuPG; btrfs and devicemapper are not used (overlay).
# seccomp, systemd and libsubid are required.
ARG BUILDTAGS="seccomp systemd libsubid containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper"

# quadlet embeds ${PREFIX}/bin as the podman path in generated units. The
# build time podman embeds is its pinned commit time, as for the Rust stage.
ARG PODMAN_PREFIX=/usr
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    set -eu; cd /src/podman; mkdir -p /out; \
    make PREFIX="${PODMAN_PREFIX}" BUILDTAGS="${BUILDTAGS}" SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" bin/podman bin/quadlet; \
    install -m0755 bin/podman /out/podman; \
    install -m0755 bin/quadlet /out/quadlet; \
    if strings -a bin/quadlet | grep -qx "/usr/local/bin"; then \
        echo "error: the quadlet binary still carries /usr/local/bin as its podman directory. Every unit it generates would name a podman that is not in this image, and the failure appears only when a container is started" >&2; exit 1; \
    fi

FROM ${MICA_BUILD_BASE} AS verify
# Each binary is a target ELF, catatonit is static; NEEDED.txt is recorded.
ARG ELF_ARCH=aarch64
COPY --from=c-build /out/ /out/
COPY --from=rust-build /out/ /out/
COPY --from=go-build /out/ /out/
RUN set -eu; cd /out; \
    for b in podman quadlet crun conmon catatonit netavark aardvark-dns; do \
        test -f "$b" || { echo "error: $b was not produced by the build" >&2; exit 1; }; \
        file -b "$b" | grep -q "ELF 64-bit.*${ELF_ARCH}" || \
            { echo "error: $b is not an ${ELF_ARCH} ELF: $(file -b "$b")" >&2; exit 1; }; \
    done; \
    file -b catatonit | grep -q 'statically linked' || \
        { echo "error: catatonit is dynamically linked. It is copied INTO containers as their init and must not depend on this image's libc" >&2; exit 1; }; \
    for b in podman quadlet crun conmon netavark aardvark-dns; do \
        objdump -p "$b" | awk -v b="$b" '/NEEDED/{print b": "$2}'; \
    done > /out/NEEDED.txt; \
    for b in podman quadlet crun conmon catatonit netavark aardvark-dns; do \
        printf '%-16s %8s KiB  %s\n' "$b" "$(( $(stat -c%s "$b") / 1024 ))" "$(file -b "$b" | cut -c1-38)"; \
    done; \
    sha256sum podman quadlet crun conmon catatonit netavark aardvark-dns > /out/SHA256SUMS

FROM scratch AS artifact
COPY --from=verify /out/ /
