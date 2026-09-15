# 20260915-1042-package-versions Packages locked by their own version

- **status**: completed
- **priority**: P1
- **owner**: sq6oxsf0
- **createdAt**: 2026-09-15 10:42

## Description

The package-version rules R0-R8 (coordinator uj991oa2, user decision
2026-09-15, `mica:docs/decisions/2026-09-15-package-versions.md`): mica-podman
declares its version and SOURCE_DATE_EPOCH in `deb/mica-podman.control`, a
release never changes them, and a release reuses an unchanged package by digest
after the inputs guard and a byte-identical rebuild. Plan:
`docs/plan/20260915-1042-package-versions.md`.

## ActiveForm

Locking mica-podman by its own version

## Dependencies

- **blocked by**: the release-lock spec change for pool annotations (R6) in mica
- **blocks**: (none)

## Notes

- Design deltas D1-D7 accepted by the coordinator (mica 1266fbf, 19fbdce).
- Done: d47ffbc (CI 34959620045); release 20260915-1057 (run 34960701595),
  verified anonymously; the next commit's CI must reuse it.

- complete: d47ffbc; release 20260915-1057
