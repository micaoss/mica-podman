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

- Landed: `tools/vectors.sh check|sync`, `tests/vectors.pin` (mica
  `735ebaa24d3a42fc339a9a0e2a6d4b76ab1d287a`), `tests/vectors-test.sh` (13),
  the `data` kind in `tools/check-lock.sh`, `make vectors` in `ci.yml`.
- The copy was 48 rows from `4df34ee` and is 53 of 84 now; the difference was
  not only the `data` family (a renamed vector and eight changed `upstream/`
  ones), which is the staleness a row count cannot show.
- Reported to the coordinator: `upstream/valid/upstream.lock` and seven of its
  neighbours at the pinned commit carry the bun asset URL
  `bun-linux-uefi-x64.zip`, which reads as collateral of the x64 -> uefi-x64
  board rename; upstream publishes `bun-linux-x64.zip`. Ours now matches mica,
  because the vectors are mica's; the fix belongs there.
