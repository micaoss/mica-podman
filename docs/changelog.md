# Changelog

## 2026-09-15 11:15 [release]

`20260915-1057` at `d47ffbc` is the first release under the package-version
rules (CI 34959620045, release run 34960701595): `mica-podman` `5.8.6-1` for
amd64 and arm64, built because `20260915-0245` predates recorded inputs; pool
manifests carry only `mica.source-repo` and `mica.arch`, and each layer its
`mica.inputs`. `SHA256SUMS` sha256 `d347fdf5...`; verified anonymously and valid
for the spec checker. mica-system-base moves to `20260915-1102` (upstream and
apt rows unchanged, base-check closure unchanged); `locks/` is not a package
input, so the package keeps its version.

## 2026-09-15 10:55 [progress]

mica-podman is locked by its own version (plan `20260915-1042-package-versions`,
`mica:docs/decisions/2026-09-15-package-versions.md` R0-R8).
`deb/mica-podman.control` declares `Version: 5.8.6-1` and
`X-Mica-Source-Date-Epoch: 1786640584`; `tools/version.sh` reads them and
requires the upstream part to be the podman tag. The engine build and the pack
use that epoch (podman, netavark and aardvark-dns embed it instead of their own
commit times), the stamp records it, and the archive carries no
Mica-Source-Commit and no `+git` version. `tools/package-inputs.sh` hashes what
decides the bytes (build-env images excluded); release pool layers record it as
`mica.inputs`, and pool manifests carry only `mica.source-repo` and `mica.arch`.
`tools/reuse.sh`, in ci.yml and at release, compares every archive with the
latest release carrying the lock: a lower version is refused, a higher one
built, the same version needs the same inputs and bytes and is reused by
digest; a release without recorded inputs is no reuse source, so the first
release under the rules builds everything. `make offline` warns that no release
was compared. Local: make check (release test 29 cases); amd64 engine, pack and
package-gate --reproduce; reuse.sh against 20260915-0245 says build.

## 2026-09-15 03:40 [progress]

mica-system-base moves to `20260915-0209` (built on mica-build-env
`20260915-0138`; 20260915-0059 and its images are deleted):
`locks/mica-system-base.lock` and its pin are replaced together (`SHA256SUMS`
sha256 `19672ed4...`); its upstream rows are the same as 0059's. On user
instruction the history is squashed into one root commit, the earlier releases,
tags, Actions runs and unreachable ghcr versions are deleted, and a new release
is cut from that root.

## 2026-09-15 02:50 [progress]

mica-build-env moves to `20260915-0138` (20260915-0030 and its images are
deleted): `locks/mica-build-env.lock` and `locks/pins/mica-build-env.pin` are
replaced together (`SHA256SUMS` sha256 `8efc21ba...`). Its image rows carry
release tags and new digests; the upstream rows are unchanged. The release
`20260915-0138` of this repository was built on the deleted images.

## 2026-09-15 02:30 [release]

`20260915-0138` at `6a15000` is the first release in the lock format: CI
34917209189, release run 34918094457. It carries exactly `mica-podman.lock`
and `SHA256SUMS` (sha256 `39b47945017d1e6f1bede9f99c686aee89150faa89735773f409860530456873`);
the pools `ghcr.io/micaoss/mica-podman:pool.{amd64,arm64}.20260915-0138` hold
`mica-podman_5.8.6+git6a150004dc49-1_{amd64,arm64}.deb`. Read back anonymously:
both assets, both pools by tag and digest, and both archives at their package
sha256; the lock is valid for `tools/check-lock.sh` and the spec's reference
checker. The package `ghcr.io/micaoss/mica-podman` is public.

## 2026-09-15 02:10 [progress]

Release lock migration, stage 3 (plan `20260915-0109-release-lock`; spec
`mica:docs/design/release-lock.md`). Inputs are `locks/`:
`mica-build-env.lock` of 20260915-0030 and `mica-system-base.lock` of
20260915-0059, unchanged, with their pins, and `locks/upstream.lock` with the
six upstream trees as `git` rows (tag and commit; the commits carry the same
`git archive` trees the removed `versions.env` pinned). `tools/check-lock.sh`
checks the file rules of locks, pins and `upstream.lock` and passes the spec's
48 lock, upstream and pins vectors (`tests/lock-test.sh`); `tools/inputs.sh`
replaces `tools/build-env.sh` (`check`, `verify`, `image`, `upstream-image`),
and every third-party image the tree names is an upstream row of the build-env
lock. `build-env-image.lock`, the four Base files, `debian-packages.lock`,
`versions.env` and `versions-stamp.sh` are gone. The Dockerfile clones each tag
and verifies its commit; `_out/podman/<arch>/upstream.lock` stamps a build, and
the package ships it as `/usr/share/mica-podman/upstream.lock` in place of
`versions.env`. `tools/base-check.sh` reads the Base rootfs `image` rows, the
`upstream` rows and the `apt` row, and takes this repository's own resolved
archives from `source` rows of `locks/upstream.lock`. `tools/release.sh` now
publishes the pools (with `mica.source-repo` and `mica.source-commit`), reads
them back anonymously, and attaches exactly `mica-podman.lock` (release, pool
and package rows, checked before upload) and `SHA256SUMS` listing it; no debs
and no `podman-pkgs.lock`. Local: make check; base-check; amd64 engine, pack and
package gate with no-cache reproduction on the new images.

## 2026-09-14 22:20 [progress]

Base moves to mica-system-base `20260914-2206` (shadow last-change day pinned):
its three assets replace the root files whole and `system-base-release` records
`SHA256SUMS` sha256 `40139bd9...`. On amd64 and arm64 the root still lacks
libatomic1, libglib2.0-0t64, libjson-c5 and libsubid5, all rows pinned for our
roots.

## 2026-09-14 21:00 [progress]

Offline build increment 1 (plan `20260914-2047-offline-build`): `make offline`
(`tools/offline.sh`) refuses a dirty tree, builds the engine and packs it for
amd64 and arm64 with only the pins already in the tree, refuses a tree that
changed during the build, and prints each pool, `Packages` and `SHA256SUMS`. The
archive is now written as an indexed pool, the layout mica-core and mica-boards
write and mica-build's `tools/local-pins.sh` reads:
`_out/debs/<arch>/pool/mica-podman_<version>_<arch>.deb`, `Packages`
(`dpkg-scanpackages` in `IMAGE_MICA_BUILD_BASE`, no network) and `SHA256SUMS`
over `pool/<archive>`. The package gate, `tools/release.sh` and both workflows
read that layout; the gate also requires the index to match the archive. Release
assets are unchanged. `tests/offline-test.sh` (4 cases) joins `make check`.

## 2026-09-14 20:40 [progress]

Base moves to mica-system-base `20260914-1931` (the root's accounts and groups
locked): its three assets replace the root files whole and `system-base-release`
records `SHA256SUMS` sha256 `0a7cb932...`. The packages lock keeps the
six-column form (42 rows); on amd64 and arm64 the root still lacks libatomic1,
libglib2.0-0t64, libjson-c5 and libsubid5, all rows pinned for our roots.

## 2026-09-14 20:10 [progress]

A release also publishes OCI artifacts and `podman-pkgs.lock` (user request;
plan `20260914-1932-oci-pool-release`). `tools/release.sh <tag>` pushes
`ghcr.io/micaoss/mica-podman:pool.<arch>.<tag>` for amd64 and arm64, the
workspace pool format with the archive as its one layer and the commit time as
`created`, refuses a pool tag that holds another manifest, and reads both back
with no credential before any asset is attached. The release assets become the
two archives, `podman-pkgs.lock` (package, architecture, version, sha256, pool by
digest; the user chose rows naming the package over `POOL_*` keys) and
`SHA256SUMS` over those three. `release.yml`'s publish job gains
`packages: write`. `tests/release-test.sh` runs against a real registry
container (distribution v3.1.1 by digest) and has 22 cases, among them the pools
and lock (R1), a rerun that pushes nothing (R2) and a pool tag holding another
manifest (R19); every refusal leaves the registry untouched.

## 2026-09-14 12:45 [progress]

Base moves to mica-system-base `20260914-1148` (built on mica-build-env
`20260914-1129`): its three assets replace the root files whole and
`system-base-release` records the tag and `SHA256SUMS` sha256 `574485b2...`.
Its `system-base-packages.lock` has a sixth column, the roots each package is
pinned for; `tools/base-check.sh` refuses any other form and takes only Base's
rows pinned for a root in `deb/debian-depends`. On amd64 and arm64 the root
still lacks libatomic1, libglib2.0-0t64, libjson-c5 and libsubid5, all such
rows; `debian-packages.lock` records nothing.

## 2026-09-14 12:20 [progress]

`build-env-image.lock` is replaced whole by the lock of mica-build-env
`20260914-1129` (all four images rebuilt; lock sha256 `2ef37b0c...`,
`SHA256SUMS` sha256 `6c582b2a...`); `make build-env` finds and verifies that
release. The engine cache key changes with the lock.

## 2026-09-14 12:05 [progress]

The engine is byte-reproducible (user decision). podman, quadlet, netavark and
aardvark-dns embedded the build clock; the Dockerfile now sets
`SOURCE_DATE_EPOCH` to each component's pinned commit time, which the
`versions.env` tree hash covers, and drops netavark's and aardvark-dns's own
build-script outputs so a cached target cannot keep an older time (a warm local
build did, with the time of a previous run). The shipped binaries change: they
carry those commit times instead of the build time. `tests/package-gate.sh
--reproduce` rebuilds the engine with no cache (`build.sh` takes
`MICA_PODMAN_OUT` and `MICA_NO_CACHE=1`) and compares all seven shipped
binaries before the archive rebuild; on amd64 locally it failed for those four
before the change and passes for all seven after it.

## 2026-09-14 11:25 [progress]

The retired project name appears nowhere (user decision, as for the former
organisation): the tracked tree and its paths had no occurrence left beyond the
package-test guard, which now spells its pattern so that it does not carry the
name itself, and also checks tracked paths.

## 2026-09-14 10:44 [progress]

The Base pin moves to the repository root, the layout used across the workspace
(user decision): `system-base.lock`, `system-base-packages.lock` and
`system-base.sources` of mica-system-base `20260914-0829` unchanged, and
`system-base-release` with the tag and the sha256 of its `SHA256SUMS`, which is
no longer committed. `system-base/` is gone. `tools/base-check.sh` downloads
`SHA256SUMS` from the release, requires the recorded hash and exactly the three
assets, and checks the committed files against it.

## 2026-09-14 09:20 [progress]

Base moves to mica-system-base `20260914-0829` and is consumed as its README
requires: `system-base/` holds the four release assets and the recorded
`SHA256SUMS` hash; `tools/base-check.sh` resolves `deb/debian-depends` with apt
against `system-base.sources` and each root's dpkg status. On amd64 and arm64
the root lacks libatomic1, libglib2.0-0t64, libjson-c5 and libsubid5, all rows of
Base's packages lock; `debian-packages.lock` records nothing.

## 2026-09-14 08:40 [progress]

The Debian packages mica-podman needs are no longer pinned here: mica-system-base
pins them (user decision). `packages/`, `sources.json` and
`tools/debian-packages.sh` are removed; `system-base.lock` and
`system-base-packages.lock` of mica-system-base release `20260914-0742` are
committed unchanged, and `tools/base-check.sh` (CI) requires all 39 packages of
the closure of `deb/debian-depends` to be in that release's root or packages
lock, on amd64 and arm64.

## 2026-09-14 07:40 [progress]

`packages/` declares the Debian runtime closure of mica-podman in
mica-system-base's pin format (39 packages on snapshot 20260905T000000Z; 35 equal
to mica-system-base's pins, new: libatomic1, libglib2.0-0t64, libjson-c5,
libsubid5), resolved from `deb/debian-depends` by `tools/debian-packages.sh`.
CI compares it with the snapshot, and the package gate requires the archives'
Debian `Depends` to be exactly `deb/debian-depends`.

## 2026-09-14 05:00 [progress]

CI and release builds restore the engine's compile caches (ccache, Go and Cargo
caches, the netavark and aardvark-dns targets) by an exact key of architecture,
`build-env-image.lock`, `versions.env` and `Dockerfile` (`tools/build-cache.sh`,
`actions/cache` v6.1.0). Only pushes to main save them, with the two crates'
own artifacts pruned; every build step and gate still runs.

## 2026-09-14 03:40 [progress]

Releases follow the workspace contract: a release `<YYYYMMDD-HHMM>` is cut on a
commit of main with `gh release create`, and `release.yml` runs on
`release: published`, builds and gates that tag, and `tools/release.sh <tag>`
attaches the archives and `SHA256SUMS` to the existing release. It no longer
creates releases or chooses a name; attached assets are accepted when identical,
missing ones are uploaded, and anything else is refused.

## 2026-09-14 03:00 [progress]

`tools/build-env.sh verify` no longer carries a release name or trusted hash:
it finds the mica-build-env release whose published `build-env-image.lock` is
the root file and checks that release's `SHA256SUMS`, so moving to another
release replaces `build-env-image.lock` only.

## 2026-09-14 02:40 [progress]

mica-podman starts in `micaoss` as one commit. It builds the container engine
from the upstream tags in `versions.env` on the images of
`build-env-image.lock` (micaoss/mica-build-env release `20260914-0128`) and
packs the Debian package `mica-podman` with its own scripts: `build.sh`,
`tools/package.sh`, `deb/`, `tests/package-gate.sh`. `ci.yml` builds and gates
pushes and pull requests; `release.yml`, dispatched by hand on main, releases
the archives as the GitHub Release `<YYYYMMDD-HHMM>` through
`tools/release.sh`. Maintainer and copyright name Mica OS; container state lives
under `/mica/containers`; `/usr/bin/docker` is an alias of podman.
