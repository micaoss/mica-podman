#!/usr/bin/env bash
# The release-lock vectors, read out of mica at a pinned commit rather than
# copied (mica:docs/design/release-lock.md 9.1: "The vectors are not copied. A
# consumer reads them out of mica at a pinned commit and refuses a
# difference"). tests/vectors.pin names the commit; tests/vectors is the tree
# tests/lock-test.sh runs tools/check-lock.sh over.
#
#   bash tools/vectors.sh check    tests/vectors is exactly the subset this repository must pass, at the pinned commit (network)
#   bash tools/vectors.sh sync     write that subset into tests/vectors (network)
#
# THE SUBSET IS DERIVED, NOT DECLARED. A reader must pass every vector for the
# forms it can encounter, and what it can encounter follows from locks/pins/,
# a fact about a directory rather than a claim about this repository's habits:
#
#   - the producers pinned there decide which kinds a lock read here may
#     carry: the kinds of those producers' own valid vectors in mica, not the
#     kinds their currently pinned locks happen to use. That difference is the
#     point. mica-system-base's next release carries `data` rows, so the data
#     vectors are ours before the re-pin, which is when the row has to be
#     implemented (1.2.4) -- deriving from the pinned bytes would have hidden
#     them until the lock that needs them was already here and refused.
#   - a kind no producer of this format emits is not a kind at all, so
#     `unknown-kind` stays ours: it is the refusal, not the form.
#   - no pin here carries SCOPE=, so no lock read here has a <scope>.<release>
#     release row, and the scoped vectors are not ours.
#   - a vector whose mode is not one of check-lock.sh's three (repos/, which
#     belongs to tools/repos.sh) has no reader here at all.
#
# No family is named in this file. Pin a scoped producer, or a producer whose
# lock carries a kind this one does not, and those vectors become required on
# the next run without an edit here.
set -euo pipefail
export LC_ALL=C
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD

VECTORS="${MICA_VECTORS:-${REPO_ROOT}/tests/vectors}"
PINFILE="${MICA_VECTORS_PIN:-${REPO_ROOT}/tests/vectors.pin}"
LOCKS="${MICA_LOCKS:-${REPO_ROOT}/locks}"
# docs/design/release-lock/vectors of the pinned commit: five path components
# to strip, the archive's own root directory included.
VPATH=docs/design/release-lock/vectors
SOURCE="${MICA_VECTORS_SOURCE:-https://codeload.github.com/micaoss/mica/tar.gz}"

die() { echo "vectors.sh: error: $*" >&2; exit 1; }
case "${1:-}" in
check | sync) [ "$#" -eq 1 ] || { echo "usage: bash tools/vectors.sh check|sync" >&2; exit 2; } ;;
*) echo "usage: bash tools/vectors.sh check|sync" >&2; exit 2 ;;
esac

mapfile -t PIN <"${PINFILE}"
[ "${PIN[0]}" = "# mica-vectors v1" ] || die "${PINFILE} does not start with '# mica-vectors v1'"
[ "${#PIN[@]}" = 3 ] || die "${PINFILE} has ${#PIN[@]} lines; it is the header, REPOSITORY and COMMIT"
REPOSITORY="${PIN[1]#REPOSITORY=}" COMMIT="${PIN[2]#COMMIT=}"
[ "${REPOSITORY}" != "${PIN[1]}" ] && [ "${COMMIT}" != "${PIN[2]}" ] || die "${PINFILE} is not REPOSITORY= then COMMIT="
[[ "${REPOSITORY}" =~ ^[a-z0-9][a-z0-9-]*$ ]] && [[ "${COMMIT}" =~ ^[0-9a-f]{40}$ ]] ||
    die "${PINFILE} names '${REPOSITORY}' at '${COMMIT}'; the commit is a full 40-character object name"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
C="${WORK}/canonical"
mkdir -p "${C}"
curl -fsSL --retry 3 --max-time 600 -o "${WORK}/vectors.tar.gz" "${SOURCE}/${COMMIT}" ||
    die "fetching ${REPOSITORY} ${COMMIT} from ${SOURCE} failed"
tar -xzf "${WORK}/vectors.tar.gz" -C "${C}" --strip-components=5 --wildcards "*/${VPATH}/*" ||
    die "${REPOSITORY} ${COMMIT} carries no ${VPATH}"
[ -f "${C}/expected.tsv" ] || die "${REPOSITORY} ${COMMIT} has ${VPATH} without an expected.tsv"

# The rows of a lock, its release field and the repository it belongs to.
kinds_of() { awk -F'\t' 'NR > 1 && $0 !~ /^#/ && NF { print $1 }' "$1"; }
release_of() { awk -F'\t' '$1 == "release" { print $3 }' "$1"; }
owner_of() { awk -F'\t' '$1 == "release" { print $2 }' "$1"; }

# What this repository pins.
PRODUCERS=() SCOPED_PIN=0
for pin in "${LOCKS}"/pins/*.pin; do
    [ -e "${pin}" ] || continue
    name="$(sed -n 's/^REPOSITORY=//p' "${pin}")"
    [ -n "${name}" ] || die "${pin} names no REPOSITORY"
    PRODUCERS+=("${name}")
    if grep -c '^SCOPE=' "${pin}" >/dev/null 2>&1; then SCOPED_PIN=1; fi
done
[ "${#PRODUCERS[@]}" -gt 0 ] || die "${LOCKS}/pins/ holds no pin, so nothing decides which vectors are required"

# The kinds of this format, and the kinds a lock of a pinned producer may
# carry -- read from that producer's own valid vectors, which show what its
# next release may carry and not only what the pinned one does.
declare -A ALL_KINDS=() OUR_KINDS=() COVERED=()
for lock in "${C}"/lock/valid/*.lock; do
    mapfile -t ks < <(kinds_of "${lock}")
    owner="$(owner_of "${lock}")"
    for k in ${ks[@]+"${ks[@]}"}; do
        ALL_KINDS["${k}"]=1
        for p in "${PRODUCERS[@]}"; do
            [ "${p}" = "${owner}" ] || continue
            OUR_KINDS["${k}"]=1
            COVERED["${p}"]=1
        done
    done
done
for p in "${PRODUCERS[@]}"; do
    [ -n "${COVERED[${p}]-}" ] ||
        die "${REPOSITORY} ${COMMIT} has no valid lock vector of ${p}, which ${LOCKS}/pins/ pins; the kinds its lock may carry cannot be derived"
done

# required <path>: is this vector a form this repository can encounter?
required() {
    local path="$1" mode="${path%%/*}" f k r locks=() pins=()
    case "${mode}" in lock | upstream | pins) ;; *) return 1 ;; esac
    if [ -d "${C}/${path}" ]; then
        mapfile -t locks < <(find "${C}/${path}" -maxdepth 1 -name '*.lock' | sort)
        mapfile -t pins < <(find "${C}/${path}/pins" -maxdepth 1 -name '*.pin' 2>/dev/null | sort)
    else
        locks=("${C}/${path}")
    fi
    for f in ${locks[@]+"${locks[@]}"}; do
        while IFS= read -r k; do
            if [ -n "${ALL_KINDS[${k}]-}" ] && [ -z "${OUR_KINDS[${k}]-}" ]; then return 1; fi
        done < <(kinds_of "${f}")
        [ "${SCOPED_PIN}" = 0 ] || continue
        while IFS= read -r r; do
            if [[ "${r}" =~ ^[a-z0-9][a-z0-9.+-]*\.[0-9]{8}-[0-9]{4}$ ]]; then return 1; fi
        done < <(release_of "${f}")
    done
    [ "${SCOPED_PIN}" = 0 ] || return 0
    for f in ${pins[@]+"${pins[@]}"}; do
        if grep -c '^SCOPE=' "${f}" >/dev/null 2>&1; then return 1; fi
    done
    return 0
}

# The tree this repository must hold: the canonical one, less what it cannot encounter.
WANT="${WORK}/want"
mkdir -p "${WANT}"
awk 'NR == 1 && /^#/' "${C}/expected.tsv" >"${WANT}/expected.tsv"
ROWS=0 SKIPPED=0
while IFS= read -r line; do
    case "${line}" in '#'* | '') continue ;; esac
    path="${line%%$'\t'*}"
    if required "${path}"; then
        printf '%s\n' "${line}" >>"${WANT}/expected.tsv"
        mkdir -p "$(dirname "${WANT}/${path}")"
        cp -r "${C}/${path}" "${WANT}/${path}"
        ROWS=$((ROWS + 1))
    else
        SKIPPED=$((SKIPPED + 1))
    fi
done <"${C}/expected.tsv"
[ "${ROWS}" -gt 0 ] || die "no vector of ${REPOSITORY} ${COMMIT} came out required; the derivation is wrong, not the copy"

OUT=() SKIP=()
for k in "${!ALL_KINDS[@]}"; do
    if [ -n "${OUR_KINDS[${k}]-}" ]; then OUT+=("${k}"); else SKIP+=("${k}"); fi
done
derivation="pins $(printf '%s\n' "${PRODUCERS[@]}" | sort | paste -sd, -)"
derivation="${derivation}; kinds $(printf '%s\n' "${OUT[@]}" | sort | paste -sd, -)"
[ "${#SKIP[@]}" = 0 ] || derivation="${derivation}; not $(printf '%s\n' "${SKIP[@]}" | sort | paste -sd, -)"
[ "${SCOPED_PIN}" = 1 ] && derivation="${derivation}; scoped" || derivation="${derivation}; unscoped"

case "$1" in
sync)
    rm -rf "${VECTORS}"
    cp -r "${WANT}" "${VECTORS}"
    echo "vectors.sh: ${VECTORS} is ${ROWS} vector(s) of ${REPOSITORY} ${COMMIT}, ${SKIPPED} not reachable here (${derivation})"
    ;;
check)
    [ -d "${VECTORS}" ] || die "${VECTORS} does not exist; bash tools/vectors.sh sync writes it"
    diff -ruN "${WANT}" "${VECTORS}" || {
        echo "vectors.sh: error: ${VECTORS} is not the vectors of ${REPOSITORY} ${COMMIT} this repository must pass (${derivation})." >&2
        echo "       A line marked - is a vector of ${REPOSITORY} missing or altered here; a line marked + is here and not in ${REPOSITORY}." >&2
        echo "       The vectors are not this repository's to edit: change them in ${REPOSITORY}, then move the commit in ${PINFILE} and run \`make vectors-sync\`." >&2
        exit 1
    }
    echo "vectors.sh: ${VECTORS} is exactly ${REPOSITORY} ${COMMIT}: ${ROWS} vector(s), ${SKIPPED} not reachable here (${derivation})"
    ;;
esac
