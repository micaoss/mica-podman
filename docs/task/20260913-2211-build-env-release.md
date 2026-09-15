# 20260913-2211-build-env-release Build on the mica-build-env images and own the scripts

- **status**: in_progress
- **priority**: P1
- **owner**: sq6oxsf0
- **createdAt**: 2026-09-13 22:11

## Description

Build mica-podman on the images of `build-env-image.lock` with this
repository's own build, package, gate and release scripts; the deb package is
the only output. Plan: `docs/plan/20260913-2211-build-env-release.md`.

## ActiveForm

Building mica-podman on the mica-build-env images

## Dependencies

- **blocked by**: (none)
- **blocks**: (none)

## Notes

- The repository starts in micaoss with a single commit carrying this state.
- Lock: micaoss/mica-build-env release 20260914-0128 (tag
  58c6cbc266215719e777240753e3a6a41a7f5e64; SHA256SUMS sha256
  c85bd9749ad08021be0d8795f6b6a4b036ed761bed87efd7ef4e1468a42c92a9; lock
  sha256 1d442b656b8f1edfcb31327adcbdaecc68412a23a31173cf8ac30c634191a37a),
  verified anonymously; the four images read anonymously for amd64 and arm64.
- Done: engine build, pack and package gates on this lock in CI; release
  20260914-0158 at 091dd1b through the former manual workflow.
- Lock moved to mica-build-env 20260914-1129 (target
  185c8fe5e2eaf57ed6eb97d36f59f50f63c4ab50; SHA256SUMS sha256
  6c582b2a6ff7a861c547623b6cec72259676d2c7851c5c7ab84c9ef94f9ce122; lock
  sha256 2ef37b0cf243b6e1c74c9f7fd5ec556d859a813bc38bb28b30a930a5c7cbb1da),
  the lock replaced whole.
- Remaining: `release.yml` now runs on `release: published` and attaches assets
  to the cut release; that hosted path has not run yet.
