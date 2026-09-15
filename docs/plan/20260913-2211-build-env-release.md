# 20260913-2211-build-env-release Build on the mica-build-env images and own the scripts

- **status**: implementing
- **createdAt**: 2026-09-13 22:11
- **approvedAt**: 2026-09-13 (coordinator a0psyi7e; user: the deb package is the only output, keep /usr/bin/docker)
- **relatedTask**: 20260913-2211-build-env-release

## Context

mica-build-env delivers public build-env images and, per release, only
`build-env-image.lock` and `SHA256SUMS`; each repository implements the build
rules in its own scripts.

## Design

1. `build-env-image.lock` at the root is the unchanged lock asset of a
   mica-build-env release and the only place the images are named.
   `tools/build-env.sh verify` derives the mica-build-env repository from the
   lock's references, finds the published release whose lock asset has this
   file's sha256, requires that release's `SHA256SUMS` to hash to the digest the
   release records and to list the lock, and compares the downloaded lock byte
   for byte; `image <IMAGE_MICA_BUILD_*>` reads one digest reference offline.
   Nothing else names the release, so an update replaces the lock only.
2. `build.sh` builds the engine on `IMAGE_MICA_BUILD_{BASE,C,GO,RUST}`; builder
   selection is `tools/buildx.sh`.
3. `tools/package.sh` and `deb/` (control, copyright, payload manifest,
   `Dockerfile` on `IMAGE_MICA_BUILD_BASE`, `pack.sh`) pack
   `mica-podman_<PODMAN_VERSION>+git<commit12>[.dirty]-1_<arch>.deb`.
4. `tests/package-gate.sh` gates identity fields, one stamp across arches,
   copyright, no conffiles, maintainer scripts, Replaces or enablement links,
   the payload manifest, and with `--reproduce` no-cache rebuilds of the engine
   (every shipped binary byte for byte) and of the archive.
5. CI: `build.yml` (reusable) runs `make check`, native per-architecture
   builds with reproduction and a gate over both; `ci.yml` calls it on push and
   pull request and publishes nothing; `release.yml` runs on `release:
   published` for the release's tag and attaches the gated archives with
   `tools/release.sh`.
6. The user cuts `gh release create <YYYYMMDD-HHMM> --target <commit of main>`;
   `tools/release.sh <tag>` requires a real UTC time, HEAD on main, the tag
   naming HEAD and the published release of this repository, then attaches both
   debs (`+` as `.`) and `SHA256SUMS`: identical attached assets are accepted,
   missing ones uploaded, other bytes, unexpected or incomplete assets refused;
   nothing is created, deleted or replaced. The tag and assets are read back
   anonymously.

7. Caches (user decision 2026-09-14, via coordinator uj991oa2: caches
   everywhere): the engine's BuildKit compile caches (ccache, Go build and
   module caches, Cargo registry and git, the netavark and aardvark-dns
   target directories) move between the builder and `_out/cache/engine` with
   `tools/build-cache.sh`, kept by `actions/cache/restore@v6` and
   `actions/cache/save@v6` under the exact key
   `engine-<arch>-<hash of build-env-image.lock, versions.env, Dockerfile>`.
   Only pushes to main save, after pruning netavark's and aardvark-dns's own
   compiled crates; pull requests and releases restore only. Every Dockerfile
   step, the package gate and the no-cache engine and pack reproductions still
   run.
8. Debian dependencies (user, 2026-09-14; mica-system-base README
   "Consuming a release"; root layout, user 2026-09-14): the root pins a
   Base release (its three assets unchanged, tag and `SHA256SUMS` hash in
   `system-base-release`, `SHA256SUMS` itself not committed);
   `tools/base-check.sh` checks the published `SHA256SUMS` against the record and
   the committed assets against it, reads the roots' dpkg status by digest and resolves `deb/debian-depends` with
   apt against `system-base.sources` alone; every archive the root lacks must be
   a row of Base's `system-base-packages.lock` (six columns, the last naming
   the roots it is pinned for) for a root in `deb/debian-depends`, or recorded
   in `debian-packages.lock`.
   The package gate requires the archives' Debian `Depends` to equal
   `deb/debian-depends`.
9. Reproducible engine (user, 2026-09-14): podman, quadlet, netavark and
   aardvark-dns embed a build time; the Dockerfile sets `SOURCE_DATE_EPOCH` to
   each component's pinned commit time (covered by its `versions.env` tree
   hash), and drops netavark's and aardvark-dns's own build-script outputs so a
   cached target cannot keep an older time. `--reproduce` rebuilds the engine
   with no cache and compares all seven shipped binaries.

## Decisions

- Channel: GitHub Release of this public repository; no OCI package.
- Package version: `versions.env` `PODMAN_VERSION` is the only version input.
- Release: cut by hand as `<YYYYMMDD-HHMM>` (UTC) on a commit of main;
  publishing it triggers `release.yml`, which attaches the assets.
- Build env: release `20260914-1129` of micaoss/mica-build-env (lock sha256
  2ef37b0c..., `SHA256SUMS` sha256 6c582b2a...).

## Open

- The hosted `release: published` path has not run yet.
- No engine build, package gate or release has run on the current lock yet.
