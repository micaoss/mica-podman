#!/usr/bin/env bash
# The release-lock vectors, read out of mica at a pinned commit rather than
# copied (mica:docs/design/release-lock.md 9.1: "The vectors are not copied. A
# consumer reads them out of mica at a pinned commit and refuses a
# difference"). tools/vectors.pin names the commit; tests/vectors is the tree
# tests/lock-test.sh runs tools/check-lock.sh over.
#
#   bash tools/vectors.sh check    tests/vectors is exactly the subset this repository must pass, at the pinned commit (network)
#   bash tools/vectors.sh sync     write that subset into tests/vectors (network)
#
# THE SUBSET IS DERIVED, NOT DECLARED, AND IT IS A FLOOR. A reader must pass
# every vector for the forms it can encounter; what it can encounter follows
# from three things, none of them a claim about this repository's habits:
#
#   WHAT IT READS -- the producers in locks/pins/. The kinds owed are the kinds
#   of those producers' own valid vectors in mica, not the kinds their
#   currently pinned locks happen to use. That difference is the point:
#   mica-system-base's next release carries `data` rows, so the data vectors
#   are ours before the re-pin, which is when the row has to be implemented
#   (1.2.4). Deriving from the pinned bytes would have hidden them until the
#   lock that needs them was already here and refused.
#
#   WHAT IT WRITES -- the kinds tools/release.sh emits, read out of the writer.
#   A producer that conforms only to what it consumes can emit a row nobody
#   downstream accepts.
#
#   WHAT ITS OWN FORMS MAY NOT BE -- the negative half, and the one a
#   consuming-only derivation misses. A refused vector is ours when the kinds
#   legitimate for the lock's OWN repository are kinds this one reads or
#   writes: what the vector carries beyond them is the defect it asserts, and
#   asserting it against a form of ours is the point.
#   `scoped-release-not-allowed` is a package producer's lock with a scope,
#   `build-only-kind` one with an `input` row, `pins/refused/scope-not-allowed`
#   a pin with SCOPE=. This repository must refuse all three, and refusing is
#   not tested by any vector that only describes what it consumes. A refused
#   vector whose home is a scoped producer is not ours: the defect is asserted
#   against a form neither read nor written here.
#
#   A kind no valid lock of this format carries is not a foreign kind but the
#   `kind-unknown` refusal itself, so those vectors stay ours. A vector whose
#   mode is not one of check-lock.sh's three (repos/, which belongs to
#   tools/repos.sh) has no reader here at all.
#
# No family is named in this file. Pin a scoped producer, or a producer whose
# lock carries a kind this one does not, and those vectors become required on
# the next run without an edit here. The derivation is a floor, not a ceiling:
# standing above it is cheap once the test runs.
set -euo pipefail
export LC_ALL=C
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD

VECTORS="${MICA_VECTORS:-${REPO_ROOT}/tests/vectors}"
PINFILE="${MICA_VECTORS_PIN:-${REPO_ROOT}/tools/vectors.pin}"
LOCKS="${MICA_LOCKS:-${REPO_ROOT}/locks}"
# docs/design/release-lock/vectors of the pinned commit: five path components
# to strip, the archive's own root directory included.
VPATH=docs/design/release-lock/vectors
SOURCE="${MICA_VECTORS_SOURCE:-https://codeload.github.com/micaoss/mica/tar.gz}"

die() { echo "vectors.sh: error: $*" >&2; exit 1; }
USAGE="usage: bash tools/vectors.sh check | sync | pin <file>"

# 9.2: the mica-vectors-pin v1 form. Prints `valid` or `refused <rule>` in the
# vocabulary of 1.5, and sets PIN_REPOSITORY and PIN_COMMIT when it is valid.
# Comment lines may follow the header and the gate acts on none of them: the
# first pin written used one to record that its commit carries a known inert
# defect, which is a thing a pin should be able to say.
PIN_REPOSITORY="" PIN_COMMIT="" PIN_VERDICT=""
pin_check() {
    local f="$1" line body=()
    [ -f "${f}" ] || { echo "error: ${f} is not a file" >&2; exit 2; }
    iconv -f UTF-8 -t UTF-8 "${f}" >/dev/null 2>&1 || { PIN_VERDICT="refused encoding"; return 1; }
    [ -s "${f}" ] && [ "$(tail -c1 "${f}" | od -An -tx1 | tr -d ' ')" = 0a ] &&
        [ "$(tr -dc '\r' <"${f}" | wc -c)" = 0 ] || { PIN_VERDICT="refused encoding"; return 1; }
    mapfile -t L <"${f}"
    [ "${L[0]}" = "# mica-vectors-pin v1" ] || { PIN_VERDICT="refused header"; return 1; }
    for line in ${L[@]+"${L[@]:1}"}; do
        case "${line}" in '#'*) ;; *) body+=("${line}") ;; esac
    done
    [ "${#body[@]}" = 2 ] && [ "${body[0]}" != "${body[0]#REPOSITORY=}" ] &&
        [ "${body[1]}" != "${body[1]#COMMIT=}" ] || { PIN_VERDICT="refused pin-format"; return 1; }
    PIN_REPOSITORY="${body[0]#REPOSITORY=}" PIN_COMMIT="${body[1]#COMMIT=}"
    [[ "${PIN_REPOSITORY}" =~ ^[a-z0-9][a-z0-9-]*$ ]] && [[ "${PIN_COMMIT}" =~ ^[0-9a-f]{40}$ ]] ||
        { PIN_VERDICT="refused field-value"; return 1; }
    PIN_VERDICT=valid
}

case "${1:-}" in
pin)
    [ "$#" -eq 2 ] || { echo "${USAGE}" >&2; exit 2; }
    pin_check "$2" || true
    echo "${PIN_VERDICT}"
    [ "${PIN_VERDICT}" = valid ]
    exit
    ;;
check | sync) [ "$#" -eq 1 ] || { echo "${USAGE}" >&2; exit 2; } ;;
*) echo "${USAGE}" >&2; exit 2 ;;
esac

pin_check "${PINFILE}" || die "${PINFILE} is ${PIN_VERDICT} as a mica-vectors-pin v1 record (9.2)"
REPOSITORY="${PIN_REPOSITORY}" COMMIT="${PIN_COMMIT}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
C="${WORK}/canonical"
mkdir -p "${C}"
curl -fsSL --retry 3 --max-time 600 -o "${WORK}/vectors.tar.gz" "${SOURCE}/${COMMIT}" ||
    die "fetching ${REPOSITORY} ${COMMIT} from ${SOURCE} failed"
tar -xzf "${WORK}/vectors.tar.gz" -C "${C}" --strip-components=5 --wildcards "*/${VPATH}/*" ||
    die "${REPOSITORY} ${COMMIT} carries no ${VPATH}"
[ -f "${C}/expected.tsv" ] || die "${REPOSITORY} ${COMMIT} has ${VPATH} without an expected.tsv"
[ -f "${C}/derived-from.tsv" ] || die "${REPOSITORY} ${COMMIT} has no derived-from.tsv; which valid vector a refused one is written against is what decides whether it is owed here (9.3)"

# The rows of a lock, its release field and the repository it belongs to.
# 1.0: a scoped release is <scope>.<release>.
SCOPED_RELEASE_RE='^[a-z0-9][a-z0-9.+-]*\.[0-9]{8}-[0-9]{4}$'
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

# What this repository writes, read out of the writer rather than declared.
mapfile -t PRODUCED < <(grep -oE "printf '[a-z]+.t" tools/release.sh | sed "s/printf '//; s/..$//" | sort -u)
[ "${#PRODUCED[@]}" -gt 0 ] ||
    die "no lock row kind could be read out of tools/release.sh; the derivation would silently drop what this repository produces"

# The kinds of this format; the kinds a lock of each repository may carry, read
# from its own valid vectors (what its NEXT release may carry, not only what
# the pinned one does); and which repositories release under a scope.
declare -A ALL_KINDS=() OUR_KINDS=() HOME=() HOME_SCOPED=() COVERED=()
for k in "${PRODUCED[@]}"; do OUR_KINDS["${k}"]=1; done
for lock in "${C}"/lock/valid/*.lock; do
    mapfile -t ks < <(kinds_of "${lock}")
    owner="$(owner_of "${lock}")"
    while IFS= read -r r; do
        [[ ! "${r}" =~ ${SCOPED_RELEASE_RE} ]] || HOME_SCOPED["${owner}"]=1
    done < <(release_of "${lock}")
    for k in ${ks[@]+"${ks[@]}"}; do
        ALL_KINDS["${k}"]=1
        HOME["${owner}"$'\x01'"${k}"]=1
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

# 9.3: the valid vector each refused lock vector is written against, declared
# by the owner rather than inferred here. It is what decides whether a refusal
# is owed: `scoped-release-not-allowed` is an edit of a package producer's
# lock and `release-slash` an edit of a board lock, and only the first is a
# form this repository has.
declare -A SIBLING=()
while IFS=$'\t' read -r refused relation sibling; do
    case "${refused}" in '#'* | '') continue ;; esac
    [ -n "${relation}" ] && [ -n "${sibling}" ] || die "derived-from.tsv row '${refused}' names no sibling"
    SIBLING["${refused}"]="${sibling}"
done <"${C}/derived-from.tsv"

# required <path> <valid|refused>: is this vector a form this repository has?
required() {
    local path="$1" result="$2" mode="${path%%/*}" f k r home locks=() pins=()
    # vectors-pin/ is read by the pin reader above, which this repository has.
    [ "${mode}" != vectors-pin ] || return 0
    case "${mode}" in lock | upstream | pins) ;; *) return 1 ;; esac
    # A refused vector is owed exactly when the valid vector it was written
    # against is owed.
    if [ "${result}" != valid ] && [ -n "${SIBLING[${path}]-}" ]; then
        required "${SIBLING[${path}]}" valid
        return
    fi
    if [ -d "${C}/${path}" ]; then
        mapfile -t locks < <(find "${C}/${path}" -maxdepth 1 -name '*.lock' | sort)
        mapfile -t pins < <(find "${C}/${path}/pins" -maxdepth 1 -name '*.pin' 2>/dev/null | sort)
    else
        locks=("${C}/${path}")
    fi
    for f in ${locks[@]+"${locks[@]}"}; do
        home="$(owner_of "${f}")"
        while IFS= read -r k; do
            # In a refused vector, a kind foreign to the lock's own repository
            # is the defect asserted, not a form to conform to.
            [ "${result}" = valid ] || [ -n "${HOME[${home}$'\x01'${k}]-}" ] || continue
            if [ -n "${ALL_KINDS[${k}]-}" ] && [ -z "${OUR_KINDS[${k}]-}" ]; then return 1; fi
        done < <(kinds_of "${f}")
        [ "${SCOPED_PIN}" = 0 ] || continue
        if [ "${result}" = valid ]; then
            while IFS= read -r r; do
                if [[ "${r}" =~ ${SCOPED_RELEASE_RE} ]]; then return 1; fi
            done < <(release_of "${f}")
        elif [ -n "${home}" ] && [ -n "${HOME_SCOPED[${home}]-}" ]; then
            return 1
        fi
    done
    { [ "${SCOPED_PIN}" = 0 ] && [ "${result}" = valid ]; } || return 0
    for f in ${pins[@]+"${pins[@]}"}; do
        if grep -c '^SCOPE=' "${f}" >/dev/null 2>&1; then return 1; fi
    done
    return 0
}

# The tree this repository must hold: the canonical one, less what it cannot encounter.
WANT="${WORK}/want"
mkdir -p "${WANT}"
awk 'NR == 1 && /^#/' "${C}/expected.tsv" >"${WANT}/expected.tsv"
grep '^#' "${C}/derived-from.tsv" >"${WANT}/derived-from.tsv"
ROWS=0 SKIPPED=0
while IFS= read -r line; do
    case "${line}" in '#'* | '') continue ;; esac
    path="${line%%$'\t'*}"
    rest="${line#*$'\t'}"
    if required "${path}" "${rest%%$'\t'*}"; then
        printf '%s\n' "${line}" >>"${WANT}/expected.tsv"
        [ -z "${SIBLING[${path}]-}" ] || grep -F "${path}	" "${C}/derived-from.tsv" >>"${WANT}/derived-from.tsv"
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
