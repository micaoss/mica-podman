# mica-podman

The container engine of Mica OS, built from pinned upstream source and packed
as the Debian package `mica-podman`, the only output of this repository. Its
inputs are `locks/` (mica:docs/design/release-lock.md): the release locks of
mica-build-env and mica-system-base with their pins, and `locks/upstream.lock`
for the upstream trees. It owns its build, pack and release scripts.

```
make inputs                   # every lock in locks/ against its pinned release (network)
MICA_ARCH=arm64 make podman   # -> _out/podman/arm64/
make pool                     # -> _out/debs/{amd64,arm64}/{pool/mica-podman_*.deb,Packages,SHA256SUMS}
make offline                  # podman + pool for both arches from a clean checkout, pinned inputs only
make package-gate             # the gate, with no-cache engine and archive rebuilds
make check                    # offline checks
```

| Binary | Role |
|---|---|
| `podman` | the engine; `docker` is an alias |
| `quadlet` | systemd generator for `.container` files |
| `crun` | OCI runtime |
| `conmon` | per-container monitor |
| `netavark` | networking |
| `aardvark-dns` | container name resolution |
| `catatonit` | container init (static) |

The package also carries `/etc/containers` (storage and network state under
`/mica/containers`) and `etc-containers-systemd.mount`, which mica-core
enables from the `container.enabled` setting.

`storage.conf`'s `mountopt = "nodev"` agrees with the mount mica-system-base
provides (`bind,private,nosuid,nodev`). It is a default, not a hardening
measure, and it is not a security boundary: the engine is rootful, so a caller
who can run podman is already root and can bind what it likes or put the
graphroot elsewhere. Do not tighten it believing it confines anything, and do
not remove it -- removing it changes what containers can do, for nothing. The
mount options exist to be the same on every board; that is a uniformity
property, not a confinement one.

Rootless is not a supported mode (2026-09-20). It is also coherent with the
access model rather than merely unimplemented: podman access implies root
implies ssh on these devices, so there is no unprivileged-user story for the
engine to serve. The package configures the system engine only: the systemd cgroup manager, a root-owned graphroot under
`/mica/containers`, the system Quadlet directory, and `libsubid5` without
`uidmap`. A container started by the operator account fails loudly in the
user-namespace setup rather than running as root, and `docker run` fails the
same way, since `/usr/bin/docker` is a symlink to podman. Supporting it would
need `uidmap` with its file capabilities intact in the composed image, a
writable home or an explicit rootless storage path, lingering for the user
manager, and unprivileged user namespaces enabled by the board kernel; it is
not supported until a test runs a container as `mica` in a composed image.

| Path | Role |
|---|---|
| `build.sh`, `Dockerfile` | the engine binaries |
| `tools/package.sh`, `deb/` | the archive: staging, payload manifest, `pack.sh` |
| `tests/package-gate.sh` | identity, payload, copyright, no conffiles or enablement, byte-identical engine and archive rebuilds |
| `tools/release.sh` | the pools on ghcr.io and `mica-podman.lock` on the GitHub Release: identity checks, never replaced, anonymous read-back |
| `tools/inputs.sh`, `tools/check-lock.sh` | `locks/`: the file rules, release verification, image references |
| `tools/dev-pins.sh`, `pins/` | the Debian build closure of the engine stages, pinned by sha256 |

## CI and releases

`ci.yml` builds, packs and gates both architectures on every push to main and
pull request, and publishes nothing. A release is cut by hand, named by the
current UTC time, on a commit of main:

```
gh release create "$(date -u +%Y%m%d-%H%M)" -R micaoss/mica-podman --target <commit of main> --notes ""
```

Publishing it runs `release.yml`: it builds and gates the release's tag, then
`tools/release.sh <tag>` publishes it:

- `ghcr.io/micaoss/mica-podman:pool.<arch>.<tag>` for amd64 and arm64, an OCI
  artifact (`application/vnd.mica.pool`) whose one layer is the archive
  (`application/vnd.mica.deb`, titled with its file name), read back with no
  credential before the lock is attached;
- on the GitHub Release, exactly `mica-podman.lock` and `SHA256SUMS` listing
  it, downloaded back with no credential.

`mica-podman.lock` is a `mica-lock v1` release lock: the release row, a `pool`
row per architecture by digest and a `package` row per architecture with the
archive's version and sha256, the layer of that pool. A pool manifest carries
only `mica.source-repo` and `mica.arch`, and each layer its title and
`mica.inputs`, so a pool whose package did not change keeps its digest. A consumer commits it
unchanged as `locks/mica-podman.lock` with its pin. It refuses a tag that is not a UTC time, a commit
not on main, a tag that does not name the built commit, and a release that is
missing, a draft or of another repository. Nothing published is replaced:
identical assets and pools are accepted, missing ones are written, and other
bytes or unexpected assets stop the job by name. GHCR creates the package
private: the first release stops at the anonymous read until the package is made
public, and the job is then rerun.

## Debian dependencies

`deb/debian-depends` lists the archive's Debian `Depends` (all but
`mica-system`); the package gate requires them to be exactly that list.
`locks/mica-system-base.lock` is the pinned Base release. `make base-check`
(CI) verifies it against its release, reads each root's dpkg status from the
rootfs image the lock names, by digest, and runs apt with the lock's `apt` row
as its only source to name the archives the root lacks; each must be an
`upstream` row of the Base lock pinned for a root named in
`deb/debian-depends`, or a `source` row of `locks/upstream.lock` under that apt
URI, which lists what this repository resolves itself (nothing today). The
package creates no system users or groups.

## Bumping a version

The package is locked by its own version
(mica:docs/decisions/2026-09-15-package-versions.md): `deb/mica-podman.control`
declares `Version: <podman version>-<revision>` and `X-Mica-Source-Date-Epoch`,
the one `SOURCE_DATE_EPOCH` of the engine build and the pack, and a release never
changes them. A podman bump sets the upstream part and resets the revision; any
other change to what the package holds (another pin, `deb/`, `overlay/`, the
build) bumps the revision. Bump the epoch together with the version.
`tools/reuse.sh` (CI and release) compares every archive with the latest release:
a lower version is refused, a higher one is built, and the same version must
carry the same `mica.inputs` (`tools/package-inputs.sh`) and rebuild to the
published bytes, which the release then reuses by digest.

Nothing in the build reads a live Debian archive. Each engine stage declares its
packages in `pins/<stage>.roots`; `make dev-pins` resolves their closure against
the mica-build-env images the stages build FROM and writes one `source` row per
archive in `locks/upstream.lock`, with the install order in `pins/<stage>.<arch>`
and the inputs it resolved against in `pins/resolved-for`. `build.sh` fetches
and verifies each archive by sha256 and the stages install them with `dpkg`;
`tools/dev-pins.sh check` refuses a build whose build-env images or snapshot are
not the ones the closure was resolved against, so a build-env move fails with
"re-resolve" instead of mixing versions.

Edit the component's `git` row of `locks/upstream.lock`: its tag and that tag's
commit. The build clones the tag and refuses another commit.
`_out/podman/<arch>/upstream.lock` and `source-date-epoch` record which pins and
epoch a build used, and the package ships `/usr/share/mica-podman/upstream.lock`;
packaging refuses a stale directory.
`make podman-pins` (weekly in CI) reports pins that have a newer upstream
release; it never edits the file.

Another mica-build-env or mica-system-base release is adopted by replacing
`locks/<repository>.lock` with its lock asset and `locks/pins/<repository>.pin`
with its tag and the sha256 of its `SHA256SUMS`, together; `make inputs` checks
both against the release. Third-party images are taken only from the
`upstream` rows of `locks/mica-build-env.lock`.

## Building

`build.sh` and `tools/package.sh` use the `default` buildx builder when it
offers the target platform, else the `mica-<arch>` docker-container builder,
which emulates the other architecture. CI builds each architecture on a native
runner. The binaries link glibc dynamically; `catatonit` is static because it
runs inside containers.
