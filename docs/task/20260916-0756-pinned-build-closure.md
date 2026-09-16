# 20260916-0756-pinned-build-closure No consumer-time apt in the build

- **status**: in_progress
- **priority**: P1
- **owner**: sq6oxsf0
- **createdAt**: 2026-09-16 07:56

## Description

The unpinned-archive finding (coordinator uj991oa2, from mica-res): the build
installed Debian packages from a live archive. Item 2 (the pack stage) landed in
9d9e410; this is item 1, the engine stages, decided as sha256-pinned source rows
in this repository's own locks/upstream.lock with a dpkg install, not a
build-env image and not Base's upstream.pkgs. Plan:
`docs/plan/20260916-0756-pinned-build-closure.md`.

## ActiveForm

Pinning the engine stages' build closure

## Dependencies

- **blocked by**: (none)
- **blocks**: (none)

## Notes

- mica-build-env 20260916-0735 is pinned first, as the coordinator required:
  the closure is resolved against the images it names.
