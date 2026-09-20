# Changelog

## 2026-09-20 14:28 [change]

The pin and the copy move together, and they moved: `74055c7`, which carries
`ddf4edc`'s bun repair and the nine fixtures `mica` has repaired since
(`c4efe00`, `653f641`). `make vectors` is what made the lag visible, and the
move is a two-file commit rather than a one-line one -- moving the pin without
the copy would have made this repository's own gate refuse, correctly.

`tests/lock-test.sh` now runs the **multiset argument** over every `reorder-of`
sibling (`mica-system-base`, 2026-09-20): a refused vector holding exactly its
sibling's rows in another order can break no rule but order. Three of them
here, all holding. It is worth having because it is the one check in this
repository with an aperture on the canonical rather than on this copy of it --
**a comparison between replicas has an aperture of zero on a defect they
share**, which is how five fixtures carried an incidental missing comment line
through every byte comparison run that afternoon.

## 2026-09-20 14:16 [change]

Three corrections to the round above, all of them from other repositories'
results, and the pin moved twice.

**The derivation was missing its negative half** (coordinator `uj991oa2`,
after `mica-core`). What a reader can encounter is not only what it consumes:
`scoped-release-not-allowed`, `build-only-kind` and
`pins/refused/scope-not-allowed` are the vectors saying what *this*
repository's own forms may not be, and a subset derived only from what is read
drops exactly the vectors that keep a reader from silently accepting something
it should refuse. This repository had the behaviour and not the vectors: it
refused a scoped release row with `field-value` and a `SCOPE=` pin with
`pin-format`, which is a refusal for the wrong reason. `tools/check-lock.sh`
now implements `release-scope` (a `<scope>.<release>` row, and a `SCOPE=` pin:
no scoped producer is pinned here and this repository publishes no scope) and
`build-only-kind` (`input`, `origin`, `built`, `index`, `product`, `bundle`,
`asset` in a lock that is not `mica-build`'s -- kinds of the format, so
carrying one is not `kind-unknown`).

**Which refused vector is owed is now declared, not inferred.** `mica`'s
`derived-from.tsv` (9.3) names the valid vector each refused one is written
against, and that is the whole rule: a refusal is owed exactly when the form
it was written against is owed. `scoped-release-not-allowed` is an edit of a
package producer's lock and `release-slash` an edit of a board lock, and only
the first is a shape this repository has. The inference this replaced got both
right and would not have stayed right.

**`make vectors` also reads what this repository writes**: the row kinds are
read out of `tools/release.sh` rather than declared, so a writer that starts
emitting a kind the reader has never conformed to changes the required set. A
producer that conforms only to what it consumes can emit a row nobody
downstream accepts.

The pin is `tools/vectors.pin` now, `mica-vectors-pin v1` (9.2): the basename
is uniform across repositories so that finding each reader's copy is one
command, and the directory is each repository's own. The seven canonical
`vectors-pin/` vectors run here too -- this tool is that family's reader --
and the pin carries a comment saying why it is at this commit.

It moved to `b2e3044`, the first commit after the bun URL repair. `735ebaa`
was pinned for six hours with the rename artefact in it; nothing downloads a
fixture, so it was inert, and the pin says so rather than replacing it
quietly.

**Provenance, re-measured by blobs after a row comparison was shown to lie.**
The old copy was `4df34ee` -- 84 of its 86 blobs identical, 0 files it had and
`mica` did not -- with two differences: `expected.tsv`, which is the subset
manifest, and `upstream/refused/other-kind.lock`, whose last row read
`pool.amd64.x` instead of `pool.amd64.20260914-2042`. That is a hand edit,
the same one `mica-core` found in its own copy, so both copies inherited it
from a common ancestor rather than diverging. It matters because that vector
exists to prove an `upstream` lock is refused for carrying a `pool` row, and
with an invalid reference in it the lock could be refused for the wrong
reason: **a refused vector that could be refused by two rules tests neither.**
The sync replaced it with `mica`'s bytes. And `lock/valid/mica-boards.lock`
was not a vector nobody has -- it existed at `4df34ee` and was renamed later,
which the row comparison could not tell apart from an invention.

63 vectors of 92 now.

## 2026-09-20 14:00 [change]

The lock vectors are no longer copied. `tools/vectors.sh` reads
`mica:docs/design/release-lock/vectors/` at the commit `tests/vectors.pin`
names and refuses any difference, in both directions and byte for byte
(`make vectors`, in `ci.yml` beside `make base-check`); `make vectors-sync`
is the only way `tests/vectors` changes. This is 9.1 of the spec and the
mechanism `mica-build:tools/deploy-pool.sh --check` already uses on
`mica-core`'s contract fixtures.

**The required subset is derived, not declared.** Nothing in
`tools/vectors.sh` names a vector family. What this repository can encounter
follows from `locks/pins/`: the kinds a pinned producer's own *valid* vector
carries are the kinds owed, no pin carries `SCOPE=` so no scoped vector is
owed, and a mode `tools/check-lock.sh` does not have (`repos/`) is owed by
nobody here. Pin a scoped producer and the scoped vectors become required on
the next run with no edit. `tests/vectors-test.sh` tests that derivation
against a synthetic `mica` over `file://` -- a fixture copy of the real
vectors would be the copy this tool exists to remove.

Two things it found on its first run, which is the argument for it:

- **48 rows was a subset and a stale copy at once, and the size said which
  it was not.** The copy came from `4df34ee`; `lock/valid/mica-boards.lock`
  had since been renamed, eight `upstream/` vectors had changed, and every
  one of those passed, because a checker that ships its own fixtures agrees
  with itself. 53 rows now, of 84.
- The `data` family was reachable and absent. `tools/check-lock.sh` gains
  the `data` kind (1.2.4: `data <name> <file> <sha256>`, key `<name>`, sorted
  last, `data-file` for two rows naming one file), because `mica-system-base`
  is pinned here and its next release carries those rows. Implemented before
  the re-pin, not before the release, which is the sequencing the spec asks
  for: nothing breaks until something re-pins, and then it breaks loudly.

The derivation is why the `data` rows arrived on time: it reads what the
producer's vector says a lock of theirs may carry, not what the lock pinned
here happens to use today. Deriving from the pinned bytes would have hidden
the kind until the lock that needs it was already here and refused.

## 2026-09-20 13:00 [note]

The graphroot mount options are a default, not a boundary (user ruling). The
mount mica-system-base provides is `bind,private,nosuid,nodev` and
`storage.conf` agrees with `mountopt = "nodev"`; both stay, and neither is a
security measure. The engine is rootful, so a caller who can run podman is
already root: the options constrain nobody who is not already unconstrained.
The note exists so the next reader does not reason from the appearance -- adding
`noexec` for consistency and removing them as useless would be wrong for the
same reason.

What the fstab and `findmnt` checks are for survives the ruling, with a
different sentence beside them: they prove that the data filesystem and its
options are the same on every board. `noexec` on one board's data would stop
containers executing anything out of the graphroot, with an identical kernel
config and nothing in a symbol table to see it.

The same sentence also grounds the rootless record of 502fcdf: podman access
implies root implies ssh, so there is no unprivileged-user story on these
devices. Rootless being unsupported is coherent with the access model, not
merely unimplemented.

## 2026-09-20 12:00 [note]

Measured on a booted uefi-x64 guest (mica-build), against the source reading of
`218ed9c`. Two corrections to keep beside it.

The controller list is not proof of a limit. `/sys/fs/cgroup/cgroup.controllers`
on that guest reads `cpuset cpu io hugetlb pids rdma misc`, and `cpu.max` is
still absent: `CONFIG_CFS_BANDWIDTH` is what creates the file, not what enables
the controller. `podman run --cpus 0.5` fails with ``crun: open `cpu.max` for
writing: No such file or directory`` and `--memory 64m` fails the same way on
`memory.max`, exactly where this repository traced them. Anyone writing a
run-time probe for "can this system bound a container" would naturally read the
controller list first, and for cpu that check answers yes while the limit
cannot be applied. Assert the knob file, not the controller name.

The prediction about `podman stats` was wrong, in the dangerous direction. This
repository predicted an empty memory figure, flagged as its weakest evidence
because it was inferred from which controller the reader reads rather than from
a run. The guest printed `0B / 2.028GB` with `MEM % 0.00%`: a usage of zero
meaning "not measured" beside a limit that is the machine's RAM. An empty field
invites a question; a plausible number does not. Also measured there: a
container runs (`podman run --rm busybox true`, exit 0), the hierarchy is
cgroup2 unified, so the v2 validation branch this repository's conclusions rest
on is the one that runs, and after a container start the ruleset carries a
`table inet netavark`, so the fallback-to-no-firewall path was not taken on that
image.

## 2026-09-20 10:30 [note]

On a kernel without `CONFIG_MEMCG` (uefi-x64 today), traced in the sources this
repository pins rather than in upstream documentation: `podman run --memory`
fails loudly and the container does not start. podman's discard-with-a-warning
path is `verifyContainerResourcesCgroupV1`
(`pkg/specgen/generate/validate_linux.go:47`); we run the cgroup v2 branch at
line 172, which discards no memory limit. crun then sends `MemoryMax` as a
transient scope property and calls `update_cgroup_resources` anyway, ending in
`write_memory` against `memory.max`, which does not exist without the
controller: ENOENT, an OCI runtime failure.

An unlimited container runs normally; only bounding memory fails, at run time
on the device. The silent half is accounting, not limits: `podman stats` reads
the same controller and reports an empty memory figure without warning. Nothing
this package ships assumes memory accounting -- no memory key in
`containers.conf`, no resource directive in `etc-containers-systemd.mount` --
so the exposed surface is a `MemoryMax=` an operator writes in their own Quadlet
unit. Whether the kernel floor gains `MEMCG` or the difference is documented is
the user's decision, with mica-boards asked what the floor costs.

## 2026-09-20 09:30 [decision]

Rootless is not supported, recorded with its evidence (asked by the coordinator
after mica-build found `/etc/subuid` and `/etc/subgid` missing from a composed
product root). The package configures the system engine only: systemd cgroup
manager, root-owned graphroot, system Quadlet, `libsubid5` and no `uidmap`. A
container run by the operator account fails in the user-namespace setup; it does
not run as root.

Measured in the published Base root this repository pins (mica-system-base
`20260915-1102`, amd64 rootfs by digest): `/etc/subuid` and `/etc/subgid` are
present and both read `mica:100000:65536`, so the composer drops them, they were
never missing at the source. What is missing everywhere is `newuidmap` and
`newgidmap` -- `uidmap` is not installed in the Base root -- and `/home/mica`,
so the ranges cannot be used even where the files survive. Supporting rootless
would need those, plus lingering and the kernel's unprivileged user-namespace
default, and is not supported until a test runs a container as `mica` in a
composed image.

## 2026-09-16 09:00 [progress]

No stage of the build reads a live Debian archive any more (the second half of
the unpinned-archive finding; plan `20260916-0855-pinned-build-closure`). The c,
rust and go stages install their Debian packages from archives pinned by sha256
as `source` rows of `locks/upstream.lock`: 40 per architecture, 15.0 MB on
amd64, resolved by `tools/dev-pins.sh` against each stage image's own dpkg
status and fetched and verified by `build.sh`. `pins/resolved-for` records the
build-env release, images and snapshot they were resolved against, and
`tools/dev-pins.sh check` (run by `build.sh` and `make check` through
`tests/dev-pins-test.sh`) refuses a build against other inputs. mica-build-env
moves to `20260916-0735` in the same step, as the closure must be resolved
against the images that are used. The snapshot is not the Base apt row's: the
build-env images carry newer packages, so apt would resolve downgrades against
them; the runtime contract stays with the declared Depends floors, which
base-check verifies against the versions Base pins. The engine binaries are
byte-identical to the ones built before, from the live archive on the previous
images, on amd64: seven of seven.

## 2026-09-16 00:20 [fix]

The pack stage installs nothing (coordinator, item 2 of the unpinned-archive
finding). `deb/mica-podman.control` declares every Debian dependency with its
floor, `${shlibs:Depends}` and `dpkg-shlibdeps` are gone, and `deb/Dockerfile`
no longer runs `apt-get` on every package build: a floor read from whatever the
archive served that day was an unpinned input inside a declared version.
`make base-check` now also requires every declared Depends to be satisfied, at
its floor, by the version the Base root ships or the Base lock pins for our
roots. The floors are the ones dpkg-shlibdeps derived (identical on amd64 and
arm64); the duplicate bare `libsystemd0` is gone. The archive bytes change, so
the version is `5.8.6-2`. The three `apt-get` blocks of the engine Dockerfile
(c, rust and go stages) stay until mica-build-env carries those packages.

## 2026-09-15 21:30 [fix]

Three `| head -n1` pipelines are gone: under `pipefail` a consumer that exits
before its input ends can kill the producer with SIGPIPE and fail the pipeline.
`tools/base-check.sh` (the SHA256 and Filename of an apt-cache record),
`tools/release.sh` (the upload Location header) and `tests/release-test.sh` (the
registry port) now read the whole output and take the first line with a
parameter expansion. `tests/shell-lint.sh`, which flagged `grep -q`, now flags
`grep -m` and `head` on the right of a pipe as well.

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
