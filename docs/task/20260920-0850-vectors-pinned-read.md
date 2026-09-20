# 20260920-0850-vectors-pinned-read Read the lock vectors out of mica at a pinned commit

- **status**: done
- **priority**: P1
- **owner**: sq6oxsf0
- **createdAt**: 2026-09-20 08:50

## Description

mica:docs/design/release-lock.md 9.1, dispatched by coordinator uj991oa2: the
vectors are not copied. This repository reads them out of `mica` at a pinned
commit and refuses a difference, in both directions, and the required subset is
**derived from `locks/pins/`** rather than declared. The `data` family is
reachable here -- `mica-system-base` is pinned and its next release carries
`data` rows -- so `tools/check-lock.sh` implements the kind before the next
re-pin, not before the Base release (1.2.4).

## ActiveForm

Reading the vectors at a pinned commit

## Dependencies

- **blocked by**: (none)
- **blocks**: the next `mica-system-base` re-pin, which will carry `data` rows

## Notes

- Landed: `tools/vectors.sh check|sync|pin`, `tools/vectors.pin` (mica
  `b2e3044ebfdef6df7e8e939e451c14eff3fc2e72`), `tests/vectors-test.sh` (18),
  the `data`, `release-scope` and `build-only-kind` rules in
  `tools/check-lock.sh`, `make vectors` in `ci.yml`. 63 vectors of 92.
- The derivation is three clauses, not one (coordinator `uj991oa2`,
  2026-09-20): what this repository pins, what it produces (the row kinds read
  out of `tools/release.sh`), and what its own forms may not be -- the last
  taken from `mica`'s `derived-from.tsv`, which declares the valid vector each
  refused one is written against. It is a floor, not a ceiling.
- The copy was 48 rows from `4df34ee`; the staleness was not only the `data`
  family (a renamed vector and eight changed `upstream/` ones), which is what
  a row count cannot show.
- Provenance re-measured by blob rather than by row, after a row comparison
  was shown to lie: `4df34ee`, 84 of 86 blobs identical, nothing here that
  `mica` did not have, and one hand edit --
  `upstream/refused/other-kind.lock` carrying `pool.amd64.x` instead of
  `pool.amd64.20260914-2042`, the same edit `mica-core` found in its own copy.
  That vector exists to prove an `upstream` lock is refused for carrying a
  `pool` row, and with an invalid reference in it the lock could be refused
  for the wrong reason: a refused vector that could be refused by two rules
  tests neither. The sync replaced it with `mica`'s bytes.
- **Why this repository keeps a copy, restated so it does not become a habit
  nobody can justify**: `make check` is the offline gate and `lock-test` is
  inside it, so the vectors must be readable with no network. `mica-boards`
  showed the other shape also runs offline -- no copy, the vectors fetched at
  the pinned commit into a git-ignored source cache whose directory name is
  the commit -- and it is the better one, because a copy in a tree can drift
  silently and a cache cannot. It is not available here: this repository has
  no source cache and no `tools/repos.sh`, which is the same absence that puts
  the `repos/` vector family outside the required subset. **If `tools/repos.sh`
  lands here for the offline build, revisit this: deleting the copy becomes
  the cheaper option rather than the more expensive one.** The reason is
  contingent on a missing tool, not on a preference.
- The bun rename artefact (`bun-linux-uefi-x64.zip`, an asset that does not
  exist) was reported and is fixed in `mica` at `ddf4edc`. `735ebaa` was
  pinned here for six hours before the move to `b2e3044`; the pin's own
  comment records why it is at that commit.
