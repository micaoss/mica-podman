# 20260915-1042-package-versions Packages locked by their own version

- **status**: in_progress
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

- Design deltas D1-D7 reported to the coordinator; D1 (the first release under
  the rules against the older +git release) awaits an answer.
