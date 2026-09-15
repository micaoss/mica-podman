# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
# check=skip=InvalidDefaultArgInFrom
# Stages, asserts and packs mica-podman (tools/package.sh). The binaries arrive
# through the `bin` context, the configuration through `overlay`. Runs at the
# target architecture on the mica-build-env base image so dpkg-shlibdeps resolves
# against that architecture's libraries.
ARG MICA_BUILD_BASE

FROM ${MICA_BUILD_BASE} AS pack

# Runtime libraries the payload links, so dpkg-shlibdeps can derive Depends.
RUN --mount=type=cache,target=/var/cache/apt,id=apt-cache-deb-podman,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,id=apt-lists-deb-podman,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    apt-get update && apt-get install -y --no-install-recommends \
        libjson-c5 libsubid5 libseccomp2 libcap2 libglib2.0-0t64

COPY pack.sh /usr/local/bin/pack.sh
COPY mica-podman.control copyright payload.manifest /in/

COPY --from=overlay etc/containers/containers.conf \
                    etc/containers/policy.json \
                    etc/containers/registries.conf \
                    etc/containers/storage.conf /in/etc/containers/
COPY --from=overlay etc/systemd/system/etc-containers-systemd.mount /in/etc/systemd/system/

COPY --from=bin podman quadlet crun conmon netavark aardvark-dns catatonit /in/bin/
COPY --from=bin upstream.lock /in/upstream.lock

ARG MICA_DEB_VERSION
ARG MICA_DEB_ARCH
ARG SOURCE_DATE_EPOCH
ARG MICA_DEB_SOURCE_REPO
ARG MICA_DEB_SOURCE_COMMIT

# Symlinks are relative so they resolve inside a staging root. No conffiles:
# the root is an immutable dm-verity image.
RUN set -eu; \
    install -d -m 0755 /stage/etc /stage/etc/containers /stage/etc/containers/systemd \
                       /stage/etc/systemd /stage/etc/systemd/system \
                       /stage/usr /stage/usr/bin /stage/usr/lib /stage/usr/lib/systemd \
                       /stage/usr/lib/systemd/system-generators \
                       /stage/usr/libexec /stage/usr/libexec/podman \
                       /stage/usr/share /stage/usr/share/doc /stage/usr/share/doc/mica-podman \
                       /stage/usr/share/mica-podman; \
    install -m 0755 /in/bin/podman /stage/usr/bin/podman; \
    install -m 0755 /in/bin/crun   /stage/usr/bin/crun; \
    for b in quadlet conmon netavark aardvark-dns catatonit; do \
        install -m 0755 "/in/bin/${b}" "/stage/usr/libexec/podman/${b}"; \
    done; \
    ln -s ../../../libexec/podman/quadlet \
          /stage/usr/lib/systemd/system-generators/podman-system-generator; \
    ln -s podman /stage/usr/bin/docker; \
    for f in containers.conf policy.json registries.conf storage.conf; do \
        install -m 0644 "/in/etc/containers/${f}" "/stage/etc/containers/${f}"; \
    done; \
    install -m 0644 /in/etc/systemd/system/etc-containers-systemd.mount \
                    /stage/etc/systemd/system/etc-containers-systemd.mount; \
    install -m 0644 /in/copyright /stage/usr/share/doc/mica-podman/copyright; \
    install -m 0644 /in/upstream.lock /stage/usr/share/mica-podman/upstream.lock

# The staged tree (mode, type, symlink target) is exactly payload.manifest.
RUN set -eu; \
    grep -v '^#' /in/payload.manifest | grep -v '^$' | LC_ALL=C sort > /tmp/declared; \
    [ -s /tmp/declared ] || { echo "error: /in/payload.manifest declares no paths" >&2; exit 1; }; \
    find /stage -mindepth 1 -printf '%#m %y /%P\t%l\n' \
        | awk -F'\t' '{ if ($2 == "") print $1; else print $1 " -> " $2 }' \
        | LC_ALL=C sort > /tmp/staged; \
    if ! diff -u /tmp/declared /tmp/staged; then \
        echo "error: the staged tree is not deb/payload.manifest ('-' declared only, '+' staged only)" >&2; \
        exit 1; \
    fi; \
    echo "podman-deb: the staged tree is exactly the $(wc -l < /tmp/declared) declared paths"

RUN set -eu; \
    pack.sh --root /stage --control /in/mica-podman.control \
            --version "${MICA_DEB_VERSION}" \
            --arch "${MICA_DEB_ARCH}" --out /out

RUN set -eu; \
    got="$(find /out -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort | tr '\n' ' ')"; \
    want="mica-podman_${MICA_DEB_VERSION}_${MICA_DEB_ARCH}.deb "; \
    [ "${got}" = "${want}" ] || { echo "error: /out holds '${got}', expected '${want}'" >&2; exit 1; }; \
    echo "podman-deb: /out holds ${want}and no other archive"

FROM scratch AS out
COPY --from=pack /out/ /
