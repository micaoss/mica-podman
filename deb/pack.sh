#!/usr/bin/env bash
# Pack a staged tree into one Debian archive (RULES.md section 6). Runs inside
# the target architecture's mica-build-env base image, from deb/Dockerfile.
#
#   SOURCE_DATE_EPOCH=<s> MICA_DEB_SOURCE_REPO=<repo> \
#   pack.sh --root <dir> --control <template> --version <v> --arch <amd64|arm64> --out <dir>
#
# The template declares the version and X-Mica-Source-Date-Epoch literally
# (tools/version.sh); both must be what the caller passes, and the epoch line
# is not carried into the archive.
set -euo pipefail

die() { echo "pack.sh: error: $*" >&2; exit 1; }

ROOT="" CONTROL="" VERSION="" ARCH="" OUT=""
while [ "$#" -gt 0 ]; do
    case "$1" in
    --root) ROOT="${2-}"; shift 2 ;;
    --control) CONTROL="${2-}"; shift 2 ;;
    --version) VERSION="${2-}"; shift 2 ;;
    --arch) ARCH="${2-}"; shift 2 ;;
    --out) OUT="${2-}"; shift 2 ;;
    *) die "unknown option '$1'" ;;
    esac
done
for v in ROOT CONTROL VERSION ARCH OUT; do
    [ -n "${!v}" ] || die "--$(echo "${v}" | tr '[:upper:]' '[:lower:]') is required"
done
[[ "${SOURCE_DATE_EPOCH:-}" =~ ^[0-9]+$ ]] || die "SOURCE_DATE_EPOCH must be set to whole seconds"
[[ "${MICA_DEB_SOURCE_REPO:-}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "MICA_DEB_SOURCE_REPO is not a repository name"
[ -d "${ROOT}" ] && [ -n "$(ls -A "${ROOT}")" ] || die "--root ${ROOT} is not a non-empty directory"
[ ! -e "${ROOT}/DEBIAN" ] || die "--root ${ROOT} already carries DEBIAN"
[ "${ARCH}" = "$(dpkg --print-architecture)" ] || die "--arch ${ARCH} in a $(dpkg --print-architecture) container"

for f in Package Version Architecture Maintainer Section Priority Description; do
    grep -c "^${f}:" "${CONTROL}" >/dev/null || die "${CONTROL} declares no ${f}"
done
for f in Installed-Size Mica-Source-Repo Mica-Source-Commit; do
    ! grep -c "^${f}:" "${CONTROL}" >/dev/null || die "${CONTROL} declares ${f}, which the packer writes"
done
[ "$(sed -n 's/^Version: //p' "${CONTROL}")" = "${VERSION}" ] || die "${CONTROL} does not declare Version: ${VERSION}"
[ "$(sed -n 's/^X-Mica-Source-Date-Epoch: //p' "${CONTROL}")" = "${SOURCE_DATE_EPOCH}" ] || die "${CONTROL} does not declare X-Mica-Source-Date-Epoch: ${SOURCE_DATE_EPOCH}"
grep -c '^Architecture: @ARCH@$' "${CONTROL}" >/dev/null || die "${CONTROL} Architecture is not @ARCH@"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
sed -e '/^X-Mica-Source-Date-Epoch: /d' -e "s|@ARCH@|${ARCH}|g" "${CONTROL}" >"${WORK}/control"
PACKAGE="$(sed -n 's/^Package: //p' "${WORK}/control")"
PKG="${WORK}/debian/${PACKAGE}"
mkdir -p "${PKG}/DEBIAN"
cp -a "${ROOT}/." "${PKG}/"
printf 'Source: %s\n\nPackage: %s\nArchitecture: %s\n' "${PACKAGE}" "${PACKAGE}" "${ARCH}" >"${WORK}/debian/control"

# Depends are declared in the template with their floors, verified against the
# versions mica-system-base pins (tools/base-check.sh). Nothing is derived here:
# a floor read from a live archive would be an unpinned input.
case "$(sed -n 's/^Depends: //p' "${WORK}/control")" in
*'${'*) die "${CONTROL} Depends carries a substitution variable; declare every dependency and its floor" ;;
esac

# Installed-Size as dpkg-gencontrol counts it: ceil(bytes/1024) per file or link, 1 per directory.
size="$(cd "${PKG}" && find . -mindepth 1 -path ./DEBIAN -prune -o -printf '%y %s\n' |
    awk '$1 == "f" || $1 == "l" { t += int(($2 + 1023) / 1024); next } { t += 1 } END { print t + 0 }')"
{
    sed '/^$/d' "${WORK}/control"
    printf 'Installed-Size: %s\nMica-Source-Repo: %s\n' "${size}" "${MICA_DEB_SOURCE_REPO}"
} >"${PKG}/DEBIAN/control"
(cd "${PKG}" && find . -path ./DEBIAN -prune -o -type f -printf '%P\0' | LC_ALL=C sort -z | xargs -0 -r md5sum) >"${PKG}/DEBIAN/md5sums"
chmod 0644 "${PKG}/DEBIAN/control" "${PKG}/DEBIAN/md5sums"

chown -Rh root:root "${PKG}"
find "${PKG}" -print0 | xargs -0 -r touch --no-dereference --date="@${SOURCE_DATE_EPOCH}"

mkdir -p "${OUT}"
DEB="${OUT}/${PACKAGE}_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "${PKG}" "${DEB}" >/dev/null

for pair in "Package=${PACKAGE}" "Version=${VERSION}" "Architecture=${ARCH}" "Installed-Size=${size}" \
    "Mica-Source-Repo=${MICA_DEB_SOURCE_REPO}"; do
    [ "$(dpkg-deb --field "${DEB}" "${pair%%=*}")" = "${pair#*=}" ] || die "${DEB} does not declare ${pair%%=*}: ${pair#*=}"
done
[ -z "$(dpkg-deb --field "${DEB}" Mica-Source-Commit X-Mica-Source-Date-Epoch)" ] || die "${DEB} carries a commit or epoch field"
case "$(dpkg-deb --field "${DEB}" Depends)" in *'${'*) die "${DEB} Depends carries an unexpanded variable" ;; esac
[ -z "$(dpkg-deb --contents "${DEB}" | awk '$2 != "root/root"')" ] || die "${DEB} carries paths not owned by root/root"
echo "pack.sh: $(basename "${DEB}") Depends: $(dpkg-deb --field "${DEB}" Depends)"
