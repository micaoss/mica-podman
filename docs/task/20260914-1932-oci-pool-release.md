# 20260914-1932-oci-pool-release Publish the OCI pool and podman-pkgs.lock with each release

- **status**: closed
- **priority**: P1
- **owner**: sq6oxsf0
- **createdAt**: 2026-09-14 19:32

## Description

User request 2026-09-14: the release flow also publishes OCI artifacts and a
`podman-pkgs.lock` file. Plan: `docs/plan/20260914-1932-oci-pool-release.md`.

## ActiveForm

Publishing the OCI pool and podman-pkgs.lock with each release

## Dependencies

- **blocked by**: (none)
- **blocks**: (none)

## Notes

- Approved 2026-09-14 with the lock as package rows. Implemented in d84aead;
  make check and CI 34888739390 passed (release-test 22/22 against a real
  registry on the runner). The anonymous read path was checked against a public
  ghcr.io pool of mica-system-base.
- On hold (coordinator, 2026-09-14): the user decided on one release format for
  all repositories (a lock/ directory with one lock per producing repository, a
  common lock format and OCI layout, an offline build tool); mica-build drafts
  the proposal. No further asset, lock or OCI format change here, and no release
  that publishes a new format without checking with the coordinator. The
  alignment with mica-system-base put to the user (debs only in the pools,
  POOL_MICA_PODMAN_<ARCH> keys, source annotations) waits for that proposal.
- Remaining: the first real release. GHCR creates `mica-podman` private, so
  that run stops at the anonymous read until the user makes the package public
  and reruns the publish job.

- close: superseded by 20260915-0109-release-lock (release lock format)
