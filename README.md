# mica-podman

The container engine of Mica OS, built from pinned upstream source for amd64 and
arm64 and packed as the Debian package `mica-podman`, the only output of this
repository. The release-lock and build rules run in `mica-build-tools`, at the
commit `locks/mica-build-tools.pin` names, through `bin/mica-tools`.

## The package

| Binary | Installed as | Role |
| --- | --- | --- |
| `podman` | `/usr/bin/podman`, `/usr/bin/docker` (symlink) | the engine; there is no Docker daemon or socket |
| `crun` | `/usr/bin/crun` | OCI runtime |
| `conmon` | `/usr/libexec/podman/conmon` | per-container monitor |
| `netavark` | `/usr/libexec/podman/netavark` | networking |
| `aardvark-dns` | `/usr/libexec/podman/aardvark-dns` | container name resolution |
| `catatonit` | `/usr/libexec/podman/catatonit` | container init, static because it runs inside containers |
| `mica-containerd` | `/usr/bin/mica-containerd` | the container supervisor and its API (below), static |

It also ships `/etc/containers` (`containers.conf`, `policy.json`,
`registries.conf`, `storage.conf`, with storage and network state under
`/mica/containers`), mica-containerd's start-up files for both inits
(`mica-containerd.service` and `/etc/init.d/mica-containerd`), and
`/usr/share/mica-podman/upstream.lock`, the upstream pins it was built from. It
enables nothing and creates no user or group: mica-core enables mica-containerd
from the `container.enabled` setting, and mica-system provides the
`/mica/containers` and state mounts. `deb/payload.manifest` is the exact list
of paths.

`Depends` are declared in `deb/mica-podman.control` with their floors and are
exactly `deb/debian-depends` plus `mica-system`; it installs on either of
mica-system-base's inits. `nftables` names the `nft` binary netavark runs, and
`libsystemd0` the library podman loads for journald where systemd runs.

## mica-containerd

Every container on the device is mica-containerd's (`mica-containerd/`, Go,
standard library only). mica-core declares containers through its API; systemd
and OpenRC only start the daemon, and take no part in a container's life.

- **Declaration**: per container, the image, command, environment, published
  ports, volumes, pids/memory/cpu limits, a health check, the containers it
  depends on, the restart policy (`no`, `on-failure`, `always`,
  `on-unhealthy`) and whether it starts at boot. Declarations and whether each
  should run are stored atomically in `/var/lib/mica/containerd/` (STATE).
- **Running**: each container is created with `podman run --replace` and the
  labels `mica.containerd=1` and `mica.containerd.spec=<hash>`; a changed
  declaration recreates it, a restart-only change does not. Its log is
  `k8s-file` under `/run/mica-containerd/logs/` (RAM, capped).
- **Restarting**: `start_retries` bounds attempts that never reached running,
  `max_restarts` those after it did; the gap steps up by `backoff_seconds` to
  `backoff_max_seconds`; exhausting either is `fatal` until it is started
  again. Whether a stopped container restarts follows from the persisted
  declaration and the exit code podman reports, never from memory: a stop
  survives a restart of the daemon, and a reboot starts what starts at boot.
  Events trigger a pass and a full pass runs every minute.
- **Health and order**: the daemon runs the health check on its interval
  (podman schedules none, on systemd either); a container waits until each
  dependency runs, or is healthy.
- **The daemon stopping leaves the containers running**, and a restarted daemon
  adopts them.

The API is JSON over `/run/mica-containerd/api.sock` (0600, root):

| Method and path | Does |
| --- | --- |
| `GET /v1/status` | the daemon |
| `GET /v1/containers`, `GET /v1/containers/{name}` | declarations with their observed state |
| `PUT /v1/containers/{name}` | declare one |
| `PUT /v1/containers` | declare exactly a set: the others are removed |
| `DELETE /v1/containers/{name}` | remove one and its container |
| `POST /v1/containers/{name}/start`, `stop`, `restart` | persisted |
| `GET /v1/containers/{name}/logs?tail=&follow=` | its log, streamed |
| `GET /v1/events` | phase changes, newline-delimited JSON |

`mica-containerd ctl` is its client for a person on the device (`ctl list`,
`ctl logs --follow web`, `ctl apply containers.json`).

## Using a release

A release carries exactly two assets:

- `mica-podman.lock` (mica-lock v1, `mica-build-tools:docs/spec/release-lock.md`): the
  release row, a `pool` row per architecture naming
  `ghcr.io/micaoss/mica-podman:pool.<arch>.<release>` by digest, and a
  `package` row per architecture with the archive's version and sha256.
- `SHA256SUMS`, listing only the lock.

A pool is an OCI artifact (`application/vnd.mica.pool`) whose one layer is the
archive (`application/vnd.mica.deb`, titled with its file name, annotated with
its `mica.inputs`). A consumer commits the lock unchanged as
`locks/mica-podman.lock` and records the release in
`locks/pins/mica-podman.pin`, or runs `bin/mica-tools locks move mica-podman <release>`,
which writes both.

## Inputs

- `locks/mica-build-env.lock`: the build-env images every stage builds `FROM`,
  and the third-party images (buildkit), by digest.
- `locks/mica-system-base.lock`: the Base release the package installs into.
  `make base-check` reads each root's dpkg status from the rootfs the lock
  names and runs apt with the lock's `apt` rows as its only sources, one deb822
  stanza per row. Every archive the root lacks for `deb/debian-depends` must be
  an `upstream` row Base pins for one of those roots, and every declared
  `Depends` floor must be met by what the root ships or Base pins.
- `locks/upstream.lock`: the upstream trees as `git` rows (tag and commit; the
  build clones the tag and refuses another commit), and the Debian build
  closure of the engine stages as `source` rows, by sha256.
- `pins/`: each stage's build packages (`<stage>.roots`), their resolved
  closure in install order (`<stage>.<arch>`), the Debian snapshot they are
  resolved from (`snapshot`: the release, updates and security pockets, not
  older than the snapshot the build-env images install from), and the
  build-env release and images they were resolved against (`resolved-for`).
  Nothing in the build reads a live archive: `build.sh` fetches every archive
  by sha256 and the stages install them with `dpkg`, and
  `tools/dev-pins.sh check` refuses a closure resolved against other images.

**Every pin is at its latest release**: the six upstream trees (podman's major
included), mica-build-tools, mica-build-env and mica-system-base. A pin is never
moved alone and never held back; `make upgrade` (`tools/upgrade.sh`) moves all
of them:

- each `git` row to the newest release tag of its tree and the commit it
  peels to (`check-pins.sh`, `make podman-pins`, lists what is behind);
- the mica-build-env and mica-system-base locks and pins, and
  `locks/mica-build-tools.pin` with `bin/mica-tools`, to their latest releases
  through `mica-tools locks update`, which verifies everything before it writes
  anything and names each move (`<input> <from> -> <to>`) or `unchanged`; it is
  also the check that these inputs are current;
- after a mica-build-env move, `pins/snapshot` to the snapshot its images
  install from and the build closure re-resolved;
- the component versions of `deb/copyright` and the control's description,
  and the package version (below).

`.github/workflows/upgrade.yml` runs it every Monday: a change is built, packed
and gated for both architectures with `make base-check`, then `main` moves to it
and it is released without a person. A red upgrade stops before `main` moves and
leaves its `upgrade/<run>` branch.

## Versions

The package is locked by its own version
(`mica-build-tools:docs/spec/package-versions.md`).
`deb/mica-podman.control` declares `Version: <podman version>-<revision>`, whose
upstream part must be the podman tag of `locks/upstream.lock`, and
`Source-Date-Epoch`, the one `SOURCE_DATE_EPOCH` of the engine build and the
pack; both are bumped together, and a release never changes them. A podman bump
sets the upstream part and resets the revision; any other change to what
decides the archive's bytes bumps the revision; `make upgrade` does both. `deb/mica-inputs` declares
those inputs, and `mica-tools inputs deb <arch>` hashes them into
`mica.inputs`.

`mica-tools pool guard` (CI and release) compares every archive with the
latest release: a lower version is refused, a higher one is new, and the same
version must carry the same `mica.inputs` and the same bytes, which the
release then reuses by digest.

## Releasing

A release is tagged with the UTC time it is cut, and only from a commit of
`main`:

```sh
gh release create "$(date -u +%Y%m%d-%H%M)" -R micaoss/mica-podman --target <commit of main> --notes ""
```

That triggers `.github/workflows/release.yml`, which builds, packs and gates
the tag natively (amd64 on `ubuntu-26.04`, arm64 on `ubuntu-26.04-arm`), then
`publish.yml` runs `tools/release.sh <tag>`:

1. `mica-tools release check`: the tag is a UTC time naming this clean commit
   of `main`;
2. `mica-tools pool guard` on each archive;
3. `mica-tools release pool`: pushes both pools and reads them back
   anonymously;
4. `mica-tools release attach`: attaches the lock and `SHA256SUMS` to the
   published release, reads them back anonymously, and notes what changed from
   the previous release.

`upgrade.yml` releases an upgrade the same way, from its own run: a release
made with the workflow's token starts no other workflow, so both call
`publish.yml`. Nothing published is ever replaced, and nothing is published
from a workstation; `.github/workflows/ci.yml` builds, packs and gates both
architectures on every push and pull request and publishes nothing.

## Design decisions

### The firewall ruleset is netavark's alone

netavark 2.1.0 picks firewalld, then nftables, then none, and runs `nft`; the
binary shipped here links no nftables library. `nftables` is in the Base root
at `Priority: important`, and **mica-system-base disables `nftables.service`**
with its own preset (`50-mica-nftables.preset`), so nothing reads
`/etc/nftables.conf` at boot.

Do not enable it. Its `ExecStart` is `nft -f /etc/nftables.conf`, whose
Debian conffile starts with `flush ruleset`, and its `ExecStop` is
`nft flush ruleset`: enabled, it wipes netavark's ruleset on reload, restart
and shutdown. `mica-build` drops `/etc/nftables.conf` from the composed root,
so enabling the unit fails loudly -- the oneshot fails on the missing file and
takes `sysinit.target` with it -- instead of silently.

### The graphroot's `nodev` is a default, not a boundary

`storage.conf`'s `mountopt = "nodev"` agrees with the mount mica-system-base
provides (`bind,private,nosuid,nodev`). The engine is rootful, so anyone who
can run podman is already root and can bind what they like or move the
graphroot. The options keep every board the same; they confine nothing. Do not
tighten them believing they do, and do not remove them.

### Rootless is not supported

podman access implies root implies ssh on these devices, so there is no
unprivileged user for the engine to serve. The package configures the system
engine only: a root-owned graphroot under `/mica/containers`, containers run
by mica-containerd as root, and `libsubid5` without `uidmap`. A container started by the operator account, or through
`/usr/bin/docker`, fails in the user-namespace setup rather than running as
root. Supporting it would take `uidmap` with its file capabilities intact in
the composed image, a writable home or a rootless storage path, lingering for
the user manager, and unprivileged user namespaces in the board kernel, and a
test that runs a container as `mica` in a composed image.

`/etc/subuid` and `/etc/subgid` in the Base root read `mica:100000:65536`, the
default `/etc/login.defs` gives the first user (`SUB_UID_MIN 100000`,
`SUB_UID_COUNT 65536`); nobody allocated it. Its one rootful reader,
`--userns=auto`, looks up the user `root-auto-userns-user` names, `containers`
by default (`vendor/go.podman.io/storage/store.go`, `userns.go`), not `mica`,
so it finds no mapping. Setting `root-auto-userns-user` in `storage.conf` is
what a rootful use would take; nothing asks for one.

### What bounds a container

A declaration with no limits is not an unbounded container.

- **Processes: 2048 on every container.** podman sets a pids limit whenever
  the caller leaves it unset and cgroups are enabled (`InitResourceLimits`,
  `pkg/specgen/resources_linux.go`), from `containers.conf`'s `pids_limit`,
  which is unset here, so the engine default `DefaultPidsLimit = 2048` applies
  (`vendor/go.podman.io/common/pkg/config/default.go`; read in podman `v6.1.3`).
- **Memory and CPU: unbounded unless the declaration asks, and the board kernel
  decides whether it can.** `memory.max` and `cpu.max` exist only with
  `CONFIG_MEMCG` and `CONFIG_CFS_BANDWIDTH`. The kernel components of all four
  boards (`ghcr.io/micaoss/mica-boards:kernel.<board>.<release>`), both
  profiles of the FIT boards included, set `MEMCG`, `CFS_BANDWIDTH` and
  `CGROUP_PIDS` to `y`. On a kernel without `MEMCG`, `podman run --memory`
  fails at the write to `memory.max`.

Not yet measured on a device: `pids.max` from inside a running container, and
`memory.max` with a `--memory` limit taking effect on a guest built from the
current board pin.

## Development

`bin/mica-tools` runs on bun 1.4.2 from `PATH`, from `MICA_BUN`, or else in the
`mica-build-env` base image of `locks/mica-build-env.lock`.

```sh
make check                     # lint, locks/, the offline tests, the closure against the build-env images
make inputs                    # every pinned lock against its release (network)
make base-check                # the Debian dependencies against the pinned Base release (network, docker)
MICA_ARCH=arm64 make podman    # the engine -> _out/podman/arm64/
make pool                      # both archives -> _out/debs/{amd64,arm64}/{pool/,Packages,SHA256SUMS,manifest.txt}
make package-gate              # the package gate, with no-cache engine and archive rebuilds
make offline                   # podman and pool for both architectures from a clean checkout
make upgrade                   # every pin to its latest release (network, docker)
```

`build.sh` and `tools/package.sh` use the `default` buildx builder when it
offers the target platform, else a `mica-<arch>` docker-container builder that
emulates the other one; CI builds each architecture on a native runner.

| File | Purpose |
| --- | --- |
| `build.sh`, `Dockerfile` | the engine binaries, from the pinned trees and build closure |
| `tools/stamp.sh` | records which pins and epoch a build used; packing refuses a stale one |
| `tools/package.sh`, `deb/` | stages and packs the archive with `mica-tools deb pack`, and indexes the pool |
| `tests/package-gate.sh` | identity, payload, Depends, copyright, no conffiles or enablement, `mica-tools pool gate`, byte-identical rebuilds |
| `mica-containerd/` | the supervisor: `make containerd-test` (in the go image), `make containerd-e2e` (on the host's rootful podman) |
| `tools/containerd-build.sh`, `tools/containerd-test.sh`, `tests/containerd-e2e.sh` | its static build, its gates, its test on real podman |
| `tools/release.sh` | publishes a release through `mica-tools` |
| `tools/base-check.sh` | the Debian dependencies against the pinned Base release |
| `tools/dev-pins.sh`, `pins/` | resolves, checks and fetches the build closure |
| `tools/upgrade.sh` | moves every pin to its latest release and bumps the package |
| `check-pins.sh` | which upstream tags have a newer release |
| `tools/buildx.sh`, `tools/build-cache.sh` | the builders, and CI's engine compile caches |
| `bin/mica-tools`, `locks/mica-build-tools.pin` | runs the pinned `mica-build-tools` |
| `tests/` | the offline tests; `tests/mica-tools-stub.sh` stands in for `bin/mica-tools` in their fixtures |
