#!/usr/bin/env bash
# Check against the file rules of mica-lock v1 and mica-pin v1
# (mica:docs/design/release-lock.md) and print `valid`, or `refused <rule>` and
# exit 1 at the first rule broken.
#
#   bash tools/check-lock.sh lock <file>        a release lock (sections 1.1 to 1.5)
#   bash tools/check-lock.sh upstream <file>    locks/upstream.lock (section 4.1)
#   bash tools/check-lock.sh pins <dir>         a locks/ directory: its locks and pins (section 4)
#
# tools/inputs.sh runs it over locks/, tools/release.sh over the lock it
# writes, tests/lock-test.sh over the specification's vectors. Registry checks
# are not file rules. Taken from mica-build-env's check-lock.sh.
set -euo pipefail
export LC_ALL=C

[ "$#" -eq 2 ] && { [ "$1" = lock ] || [ "$1" = upstream ] || [ "$1" = pins ]; } || { echo "usage: bash tools/check-lock.sh lock|upstream|pins <path>" >&2; exit 2; }
MODE="$1"
FILE="$2"
SELF="${BASH_SOURCE[0]}"

refuse() { echo "refused $1"; exit 1; }

# 4: every lock of a producer has exactly one pin, every pin its lock.
if [ "${MODE}" = pins ]; then
    [ -d "${FILE}" ] || { echo "error: ${FILE} is not a directory" >&2; exit 2; }
    for pin in "${FILE}"/pins/*.pin; do
        [ -e "${pin}" ] || continue
        name="$(basename "${pin}" .pin)"
        iconv -f UTF-8 -t UTF-8 "${pin}" >/dev/null 2>&1 || refuse encoding
        [ -s "${pin}" ] && [ "$(tail -c1 "${pin}" | od -An -tx1 | tr -d ' ')" = 0a ] && [ "$(tr -dc '\r' <"${pin}" | wc -c)" = 0 ] || refuse encoding
        mapfile -t P <"${pin}"
        [ "${P[0]}" = "# mica-pin v1" ] || refuse header
        keys="$(printf '%s\n' "${P[@]:1}" | sed 's/=.*//' | paste -sd' ' -)"
        # A scoped pin is a well-formed mica-pin v1 record of a scoped producer
        # (mica-boards, mica-build). This repository pins neither, so a SCOPE=
        # here is a scope where none may be, not a malformed pin.
        case "${keys}" in "REPOSITORY SCOPE "*) refuse release-scope ;; esac
        [ "${keys}" = "REPOSITORY RELEASE SHA256SUMS" ] || [ "${keys}" = "REPOSITORY RELEASE SHA256SUMS CHECKOUT" ] || refuse pin-format
        repository="${P[1]#REPOSITORY=}" release="${P[2]#RELEASE=}" sums="${P[3]#SHA256SUMS=}" checkout="${P[4]:-}"; checkout="${checkout#CHECKOUT=}"
        { [ "${release}" = offline ] && [ "${#P[@]}" = 5 ]; } || { [ "${release}" != offline ] && [ "${#P[@]}" = 4 ]; } || refuse pin-format
        [[ "${repository}" =~ ^[a-z0-9][a-z0-9-]*$ ]] && { [[ "${release}" =~ ^[0-9]{8}-[0-9]{4}$ ]] || [ "${release}" = offline ]; } &&
            [[ "${sums}" =~ ^[0-9a-f]{64}$ ]] && { [ "${#P[@]}" = 4 ] || [[ "${checkout}" == /* ]]; } || refuse field-value
        [ "${repository}" = "${name}" ] || refuse name-mismatch
        [ -f "${FILE}/${name}.lock" ] || refuse pin-without-lock
        [ "$(bash "${SELF}" lock "${FILE}/${name}.lock" 2>&1 || true)" = valid ] || refuse lock-invalid
        row="$(grep -m1 "^release"$'\t' "${FILE}/${name}.lock")"
        [ "$(cut -f2 <<<"${row}")" = "${name}" ] || refuse lock-invalid
        [ "$(cut -f3 <<<"${row}")" = "${release}" ] || refuse release-mismatch
        [ "${#P[@]}" = 4 ] || [ -z "${CI:-}${GITHUB_ACTIONS:-}" ] || refuse checkout-in-ci
    done
    for lock in "${FILE}"/*.lock; do
        [ -e "${lock}" ] || continue
        name="$(basename "${lock}" .lock)"
        [ "${name}" = upstream ] || [ -f "${FILE}/pins/${name}.pin" ] || refuse lock-without-pin
    done
    echo valid
    exit 0
fi
[ -f "${FILE}" ] || { echo "error: ${FILE} is not a file" >&2; exit 2; }

KINDS=(release image pool package board upstream apt data)
declare -A COLUMNS=([release]=4 [image]=5 [pool]=3 [package]=5 [board]=4 [upstream]=7 [apt]=5 [data]=4)
kind_index() { local i; for i in "${!KINDS[@]}"; do [ "${KINDS[$i]}" != "$1" ] || { echo "$i"; return; }; done; }

# 1.1: UTF-8, LF with a final LF, no CR, header, no empty line, leading space or trailing tab.
iconv -f UTF-8 -t UTF-8 "${FILE}" >/dev/null 2>&1 || refuse encoding
[ -s "${FILE}" ] && [ "$(tail -c1 "${FILE}" | od -An -tx1 | tr -d ' ')" = 0a ] || refuse encoding
[ "$(tr -dc '\r' <"${FILE}" | wc -c)" = 0 ] || refuse encoding
mapfile -t LINES <"${FILE}"
[ "${LINES[0]}" = "# mica-lock v1" ] || refuse header

ROWS=()
for line in "${LINES[@]:1}"; do
    [ -n "${line}" ] || refuse encoding
    case "${line}" in ' '*) refuse encoding ;; *$'\t') refuse encoding ;; '#'*) continue ;; esac
    ROWS+=("${line}")
done

# split <row>: FIELDS, split on every tab (empty fields kept).
split() {
    local rest="$1"
    FIELDS=()
    while [[ "${rest}" == *$'\t'* ]]; do
        FIELDS+=("${rest%%$'\t'*}")
        rest="${rest#*$'\t'}"
    done
    FIELDS+=("${rest}")
}

NAME_RE='^[a-z0-9][a-z0-9.+-]*$'
# An upstream image keeps its original name and reference, as debian:trixie-slim.
UPSTREAM_NAME_RE='^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9._-]+)?$'
UPSTREAM_REFERENCE_RE='^[a-z0-9-]+(\.[a-z0-9-]+)+(:[0-9]+)?/[a-z0-9._/-]+(:[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$'
REPOSITORY_RE='^[a-z0-9][a-z0-9-]*$'
VERSION_RE='^[A-Za-z0-9.+~:-]+$'
SHA_RE='^[0-9a-f]{64}$'
ARCH_RE='^(amd64|arm64)$'

# upstream_image: the fields of an `image upstream` row in FIELDS; an upstream
# image by digest, never republished on one of Mica's own registries.
upstream_image() {
    [[ "${FIELDS[2]}" =~ ${UPSTREAM_NAME_RE} ]] && [[ "${FIELDS[3]}" =~ ^(index|amd64|arm64|386)$ ]] || refuse field-value
    [[ "${FIELDS[4]}" == *@sha256:* ]] || refuse reference-digest
    case "${FIELDS[4]}" in ghcr.io/micaoss/* | local/*) refuse reference-upstream ;; esac
    [[ "${FIELDS[4]}" =~ ${UPSTREAM_REFERENCE_RE} ]] || refuse field-value
}

# 4.1: no release row; image, source and git rows only.
if [ "${MODE}" = upstream ]; then
    declare -A UCOLUMNS=([image]=5 [source]=6 [git]=5)
    UKINDS=(image source git)
    for row in ${ROWS[@]+"${ROWS[@]}"}; do
        split "${row}"
        [ "${FIELDS[0]}" != release ] || refuse upstream-release-row
        [ -n "${UCOLUMNS[${FIELDS[0]}]-}" ] || refuse kind-unknown
        [ "${#FIELDS[@]}" = "${UCOLUMNS[${FIELDS[0]}]}" ] || refuse column-count
    done
    declare -A KEYS=()
    SORTKEYS=()
    for row in ${ROWS[@]+"${ROWS[@]}"}; do
        split "${row}"
        kind="${FIELDS[0]}"
        case "${kind}" in
        image)
            [ "${FIELDS[1]}" = upstream ] || refuse image-source
            upstream_image
            key="${FIELDS[1]}"$'\x01'"${FIELDS[2]}"$'\x01'"${FIELDS[3]}"
            ;;
        source)
            [[ "${FIELDS[1]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[2]}" =~ ^(amd64|arm64|all)$ ]] && [[ "${FIELDS[3]}" =~ ${VERSION_RE} ]] &&
                [[ "${FIELDS[4]}" =~ ${SHA_RE} ]] && [[ "${FIELDS[5]}" == https://* ]] || refuse field-value
            key="${FIELDS[1]}"$'\x01'"${FIELDS[2]}"
            ;;
        git)
            [[ "${FIELDS[1]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[2]}" == https://* ]] && [ -n "${FIELDS[3]}" ] && [[ "${FIELDS[4]}" =~ ^[0-9a-f]{40}$ ]] || refuse field-value
            key="${FIELDS[1]}"
            ;;
        esac
        [ -z "${KEYS[${kind}$'\x02'${key}]-}" ] || refuse duplicate-key
        KEYS["${kind}"$'\x02'"${key}"]=1
        for i in "${!UKINDS[@]}"; do [ "${UKINDS[$i]}" != "${kind}" ] || SORTKEYS+=("${i}"$'\x01'"${key}"); done
    done
    for ((i = 1; i < ${#SORTKEYS[@]}; i++)); do
        [[ ! "${SORTKEYS[$((i - 1))]}" > "${SORTKEYS[$i]}" ]] || refuse sort-order
    done
    echo valid
    exit 0
fi

# The kinds only a mica-build lock may carry (1.2.2, 1.2.3). They are kinds of
# the format, so a lock carrying one is refused for carrying it where it may
# not be, not for naming something unknown. No mica-build lock is read here, so
# the repository does not have to be checked before refusing.
BUILD_ONLY=(input origin built index product bundle asset)
for row in ${ROWS[@]+"${ROWS[@]}"}; do
    split "${row}"
    for k in "${BUILD_ONLY[@]}"; do [ "${k}" != "${FIELDS[0]}" ] || refuse build-only-kind; done
    [ -n "${COLUMNS[${FIELDS[0]}]-}" ] || refuse kind-unknown
    [ "${#FIELDS[@]}" = "${COLUMNS[${FIELDS[0]}]}" ] || refuse column-count
done

releases=0
for row in ${ROWS[@]+"${ROWS[@]}"}; do [ "${row%%$'\t'*}" != release ] || releases=$((releases + 1)); done
[ "${#ROWS[@]}" -gt 0 ] && [ "${ROWS[0]%%$'\t'*}" = release ] && [ "${releases}" = 1 ] || refuse release-row

split "${ROWS[0]}"
REPOSITORY="${FIELDS[1]}" RELEASE="${FIELDS[2]}"
# 1.0: a scoped release is <scope>.<release>, and only mica-boards and
# mica-build have one. Neither is pinned here and neither is this repository,
# so any scoped release row is a scope where none may be.
[[ ! "${RELEASE}" =~ ^[a-z0-9][a-z0-9.+-]*\.[0-9]{8}-[0-9]{4}$ ]] || refuse release-scope
[[ "${REPOSITORY}" =~ ^[a-z0-9][a-z0-9-]*$ ]] && { [[ "${RELEASE}" =~ ^[0-9]{8}-[0-9]{4}$ ]] || [ "${RELEASE}" = offline ]; } &&
    [[ "${FIELDS[3]}" =~ ^[0-9a-f]{40}$ ]] || refuse field-value
REGISTRY=ghcr.io/micaoss
[ "${RELEASE}" != offline ] || REGISTRY=local

# reference <ref> [<repository>]: a reference to <repository> (default the release's) by digest.
reference() {
    [[ "$1" == *@sha256:* ]] || refuse reference-digest
    [[ "$1" =~ ^(ghcr\.io/micaoss|local)/([a-z0-9][a-z0-9-]*)(:[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$ ]] || {
        case "$1" in ghcr.io/micaoss/* | local/*) refuse field-value ;; esac
        refuse reference-registry
    }
    [ "${BASH_REMATCH[1]}" = "${REGISTRY}" ] || refuse reference-registry
    [ "${BASH_REMATCH[2]}" = "${2:-${REPOSITORY}}" ] || refuse reference-repository
}

declare -A KEYS=() POOLS=() DATA_FILES=()
SORTKEYS=() PACKAGE_ARCHES=() BASE_ONLY=0
for row in "${ROWS[@]:1}"; do
    split "${row}"
    kind="${FIELDS[0]}"
    case "${kind}" in
    image)
        if [ "${FIELDS[1]}" = upstream ]; then
            upstream_image
        elif [[ "${FIELDS[1]}" =~ ${REPOSITORY_RE} ]]; then
            [[ "${FIELDS[2]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[3]}" =~ ^(index|amd64|arm64|386)$ ]] || refuse field-value
            reference "${FIELDS[4]}" "${FIELDS[1]}"
            # A producer's own lock names only its own images besides upstream ones.
            [ "${FIELDS[1]}" = "${REPOSITORY}" ] || refuse image-source
        else
            refuse image-source
        fi
        key="${FIELDS[1]}"$'\x01'"${FIELDS[2]}"$'\x01'"${FIELDS[3]}"
        ;;
    pool)
        [[ "${FIELDS[1]}" =~ ${ARCH_RE} ]] || refuse field-value
        reference "${FIELDS[2]}"
        key="${FIELDS[1]}"
        POOLS["${FIELDS[1]}"]=1
        ;;
    package)
        [[ "${FIELDS[1]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[2]}" =~ ${ARCH_RE} ]] && [[ "${FIELDS[3]}" =~ ${VERSION_RE} ]] && [[ "${FIELDS[4]}" =~ ${SHA_RE} ]] || refuse field-value
        key="${FIELDS[1]}"$'\x01'"${FIELDS[2]}"
        PACKAGE_ARCHES+=("${FIELDS[2]}")
        ;;
    board)
        [[ "${FIELDS[1]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[2]}" =~ ${ARCH_RE} ]] || refuse field-value
        reference "${FIELDS[3]}"
        key="${FIELDS[1]}"
        ;;
    upstream)
        roots="${FIELDS[6]}"
        sorted="$(tr ',' '\n' <<<"${roots}" | sort -u | paste -sd, -)"
        ok=1
        for r in ${roots//,/ }; do [[ "${r}" =~ ${NAME_RE} ]] || ok=0; done
        [[ "${FIELDS[1]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[2]}" =~ ${ARCH_RE} ]] && [[ "${FIELDS[3]}" =~ ${VERSION_RE} ]] &&
            [[ "${FIELDS[4]}" =~ ${SHA_RE} ]] && [[ "${FIELDS[5]}" == https://* ]] && [ "${ok}" = 1 ] && [ "${sorted}" = "${roots}" ] || refuse field-value
        key="${FIELDS[1]}"$'\x01'"${FIELDS[2]}"
        BASE_ONLY=1
        ;;
    apt)
        [[ "${FIELDS[1]}" == https://* ]] && [ -n "${FIELDS[2]}" ] && [ -n "${FIELDS[3]}" ] && [[ "${FIELDS[4]}" == /* ]] || refuse field-value
        key=""
        BASE_ONLY=1
        ;;
    # 1.2.4: a release asset a producer computed about its own output, keyed
    # by the producer's own name for it. Any repository may publish one, so it
    # is not base-only; nothing here may read one as a build input.
    data)
        [[ "${FIELDS[1]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[2]}" =~ ${NAME_RE} ]] && [[ "${FIELDS[3]}" =~ ${SHA_RE} ]] || refuse field-value
        [ -z "${DATA_FILES[${FIELDS[2]}]-}" ] || refuse data-file
        DATA_FILES["${FIELDS[2]}"]=1
        key="${FIELDS[1]}"
        ;;
    *) refuse release-row ;;
    esac
    [ -z "${KEYS[${kind}$'\x02'${key}]-}" ] || refuse duplicate-key
    KEYS["${kind}"$'\x02'"${key}"]=1
    SORTKEYS+=("$(kind_index "${kind}")"$'\x01'"${key}")
done

[ "${REPOSITORY}" = mica-system-base ] || [ "${BASE_ONLY}" = 0 ] || refuse base-only-kind
for a in ${PACKAGE_ARCHES[@]+"${PACKAGE_ARCHES[@]}"}; do [ -n "${POOLS[${a}]-}" ] || refuse package-without-pool; done
for ((i = 1; i < ${#SORTKEYS[@]}; i++)); do
    [[ ! "${SORTKEYS[$((i - 1))]}" > "${SORTKEYS[$i]}" ]] || refuse sort-order
done
echo valid
