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

Every binary is built for size and carries no symbols: podman is linked with
`-s -w`, netavark and aardvark-dns are built with opt-level `z`, fat LTO and
one codegen unit, and crun, conmon and catatonit are stripped. Installed, the
package is about 35% smaller than with the upstream defaults: Installed-Size
62,907 KiB on amd64 (96,680 before) and 57,557 KiB on arm64 (91,537). What is lost is debugging: no symbolized stack traces from
gdb, delve or `RUST_BACKTRACE` (a Go panic still names its functions), and
netavark aborts on a panic instead of unwinding (it is one short command per
call; aardvark-dns, a resident server, still unwinds). Rust code at opt-level
`z` is slower; netavark runs once per network change and aardvark-dns serves
little traffic here.

`Depends` are declared in `deb/mica-podman.control` with their floors and are
exactly `deb/debian-depends` plus `mica-system`; it installs on either of
mica-system-base's inits. `Conflicts: mica-docker`: a product carries one
container engine, and the Docker CLI of mica-docker would take
`/usr/bin/docker`. `nftables` names the `nft` binary netavark runs, and
`libsystemd0` the library podman loads for journald where systemd runs.

## mica-containerd

Every container on the device is mica-containerd's (`mica-containerd/`, Go).
mica-core declares them as Compose projects through its API; systemd and
OpenRC only start the daemon, and take no part in a container's life. A
container a person runs by hand (`docker run`) carries no `mica.containerd`
label and is neither managed nor touched.

- **Declaration**: a project is one Compose file (the subset below); each of
  its services becomes one container, named `<project>-<service>-1` or its
  `container_name`. A service is the image, command, entrypoint, environment,
  published ports, volumes, devices, its network, added and dropped
  capabilities, pids/memory/cpu limits, a health check, the services it depends on, and its
  restart policy. Projects, and whether each container should run,
  are stored atomically in `/var/lib/mica/containerd/` (STATE).
- **Running**: each container is created with `podman run --replace` and the
  labels `mica.containerd=1`, `mica.containerd.project=<project>` and
  `mica.containerd.spec=<hash>`, on the project network `<project>_default`,
  where its container name resolves and its service name is an alias, or on
  the host's network or none; a changed service recreates its
  container, a restart-only change does not. Its log is `k8s-file` under
  `/run/mica-containerd/logs/` (RAM, capped).
- **Restarting**: a run is confirmed after 1 s; 3 attempts bound a container
  that never reached running, and `on-failure:N` bounds the restarts of one
  that did; the gap steps up by 1 s to 60 s, and a run longer than 60 s resets
  the tallies; exhausting either is `fatal` until it is started again.
  Whether a stopped container restarts follows from the persisted
  declaration and the exit code podman reports, never from memory: a stop
  survives a restart of the daemon, and a reboot starts every service again.
  Events trigger a pass and a full pass runs every minute.
- **Health and order**: the daemon runs the health check on its interval
  (podman schedules none, on systemd either); a container waits until each
  dependency runs, or is healthy.
- **A name held by hand**: a project naming a container that a container
  without the label holds is refused (400, `name`, the holder's ID in the
  detail), as is one naming another project's container. A container run by
  hand later under a declared name is never replaced either: the service is
  `waiting`, its `error` names the holder, until a person removes or renames
  it.
- **The daemon stopping leaves the containers running**, and a restarted daemon
  takes them up again as they are.
- **Every container is a project's service.** A pass removes every container
  with the label that nothing declares.

### Compose projects

A project is parsed with `github.com/compose-spec/compose-go/v2`, the parser
of `docker compose`, then held to the subset mica-containerd carries out.
Anything else written in the file is refused, never ignored, so a file is
either run as written or not at all. Keys starting `x-` are ignored.

- **Project**: its name is the API path, 1 to 40 of `[a-z0-9_-]` starting
  with a letter or digit; a top-level `name` must be the same. `ctl up` and
  `compose check` name it as docker compose does: `-p`, else the file's
  `name`, else the project directory's name made a project name (lower case,
  other characters dropped). Service names are 1 to 20 of `[a-z0-9_-]`, so the
  container name stays a valid one. Top-level `services` and `volumes` are
  read; `networks` may only name `default` without options; `secrets`,
  `configs`, `include` and `extends` are refused; `version` is obsolete and
  ignored, as compose ignores it.
- **The project directory** is where a relative host path of a volume or an
  `env_file` is resolved, as in docker compose (`./data`, `../shared`): the
  directory declared with the project (`projectDir`; the Compose file's
  directory for `ctl up` and `compose check`), else `/mica/volumes/<project>/`
  (DATA). Any host path is accepted but a `~` one (there is no home
  directory). Links are resolved when the project is declared: a bind names
  the directory they lead to. A missing host directory of the short volume
  form is made, as compose makes it; one that cannot be made (a read-only
  filesystem, a file in the way) is refused with its path, and a missing one
  of the long form is refused. Named volumes are under
  `/mica/volumes/<project>/` wherever the project directory is.
- **Interpolation** (`${VAR}`, `${VAR:-default}`) runs against an empty
  environment: a default applies, and a variable without one is refused
  (`interpolation`). There is no `.env` file.

| Compose | Becomes | Refused |
| --- | --- | --- |
| `image` | the image | missing, or `build` |
| `command`, `entrypoint` | podman's command and `--entrypoint`, string forms split as compose does | |
| `environment` (map or list) | the environment | a bare `KEY` (nothing to pass through) |
| `env_file` (path, or `{path, required}`) | read when the project is declared and folded into the environment, `environment` winning; a changed file takes effect on the next declaration | a missing file that is `required` (the default), a `~` path |
| `ports`: `"8080:80"`, `"8080:80/udp"`, ranges, long form `{target, published, protocol}` | published ports, every address of the host | no published port, a `host_ip`, `mode: host`, a protocol other than tcp or udp |
| `volumes`: a host path | a bind mount of the path, its links resolved; `:ro` or `read_only` | a `~` path, a missing path that cannot be made, a missing path of the long form, bind options |
| `volumes`: a named volume `data:/x` | the directory `/mica/volumes/<project>/data`, made when missing | an anonymous volume, `tmpfs`, `npipe`, `cluster`, `nocopy` and other volume options |
| top-level `volumes: {data: {}}` | names a volume | `driver`, `driver_opts`, `external`, `labels`, `name` |
| `restart`: `no`, `always`, `unless-stopped`, `on-failure`, `on-failure:N` | `no`, `always`, `always`, `on-failure`, `on-failure` with `max_restarts: N` | any other value |
| `depends_on` (list, or map with `condition`) | started after: `service_started` waits until it runs, `service_healthy` until it is healthy | `service_completed_successfully`, `required: false`, `restart: true`, a service the project lacks |
| `healthcheck`: `test` as `[CMD, ...]`, `[CMD-SHELL, cmd]` or a string; `interval`, `timeout`, `retries`, `start_period` | the health check, a shell form run as `/bin/sh -c`; unset values are 30s, 10s, 3, 0s; `test: [NONE]` or `disable: true` is none | `start_interval`, a duration not of whole seconds, a timeout above the interval |
| `mem_limit`, `cpus`, `pids_limit`, or `deploy.resources.limits` (`memory`, `cpus`, `pids`) | the limits | both forms naming different values, `deploy.resources.reservations`, any other `deploy` key but `replicas: 1` |
| `networks` of a service: absent, `[default]` | the project network | any other network, `aliases`, addresses, `networks` beside `network_mode` |
| `network_mode`: `host`, `none` | podman's `--network host` or `none`: no project network, no alias | any other mode (`bridge`, `service:<name>`, `container:<name>`), `ports` with `host` (it listens on the host) |
| `container_name` | the container's name, and its DNS name on the project network | a name that is not 1 to 63 of `[a-z0-9_.-]` starting with a letter or digit, one another service of the file has; at declaration, one another project's container or a container run by hand holds |
| `cap_add`, `cap_drop` | podman's `--cap-add`, `--cap-drop`, written as `CAP_<NAME>`: a name in any case, with or without `CAP_`, or `ALL` | a capability the kernel does not define |
| `devices`: `/dev/ttyUSB0`, `/dev/ttyUSB0:/dev/ttyS1`, `/dev/kvm:/dev/kvm:rwm`, long form `{source, target, permissions}` | podman's `--device`; the target is the source, the permissions `rwm`, unless given. A link such as `/dev/serial/by-id/...` is kept and resolved to its node at each start, so the container is run anew at each start; a device gone then fails the start with its path, under the restart policy, and the service runs again once it is back and started | a device absent when the project is declared, a path that is no device, a relative path, permissions other than of `r`, `w` and `m` |
| `device_cgroup_rules`: `"c 188:* rwm"` | podman's `--device-cgroup-rule` | a rule not of `a`, `b` or `c`, `<major or *>:<minor or *>` and permissions of `rwm` |
| `user`, `working_dir` | podman's `--user` and `--workdir` | a user other than a name or uid with an optional `:group` or `:gid`; a working directory that is not a clean absolute path |
| `expose` | nothing: every port is reachable on the project network | |

Every other service key is refused by its name (`privileged`, `labels`,
`security_opt`, `volumes_from`, `extra_hosts`, ...).

Every service starts when its project is declared and at every boot; a
`stop` through the API holds until a `start`, a restart of the daemon
included, and a reboot starts it again. So `unless-stopped` is `always`.

### The API

JSON over `/run/mica-containerd/api.sock` (0600, root):

| Method and path | Does |
| --- | --- |
| `GET /v1/status` | the daemon |
| `GET /v1/projects` | every project: `{"projects": [{"name", "projectDir", "services": [<container>...]}]}` |
| `GET /v1/projects/{project}` | one, with its Compose text: `{"name", "projectDir", "compose", "services"}` |
| `PUT /v1/projects/{project}?projectDir=DIR` | declare one; the body is the Compose file (YAML); `projectDir`, an absolute path, is optional |
| `PUT /v1/projects` | declare exactly a set, `{"projects": {"<project>": "<compose text>" or {"compose": "<compose text>", "projectDir": "DIR"}, ...}}`: every other project is removed with its containers and network |
| `DELETE /v1/projects/{project}` | remove one, its containers and its network; its volume directories stay |
| `GET /v1/containers`, `GET /v1/containers/{name}` | every project's containers, or one, with the observed state |
| `POST /v1/containers/{name}/start`, `stop`, `restart` | persisted |
| `GET /v1/containers/{name}/logs?tail=&follow=` | its log, streamed |
| `GET /v1/events` | phase changes, newline-delimited JSON |

A container entry is `{"project", "service", "spec", "desired", "phase",
...}`; `spec` is what the service became. A project's `projectDir` is the
one it was declared with, else `/mica/volumes/<project>`. A set is all or
nothing: one refused project refuses the whole set and changes nothing. micad sends its
projects as one set at start-up and after every change of its settings.

A refusal is HTTP 400 with every problem of the file:

```json
{"error": "compose", "detail": "web: privileged is not supported",
 "problems": [{"service": "web", "field": "privileged", "detail": "privileged is not supported"}]}
```

`service` is empty for a project-level problem; `detail` is the problems
joined with `; `.

`mica-containerd compose check [-p NAME] [-f FILE | FILE] [--project-dir DIR]`
holds a file to the same rules with no daemon: exit 0 and the containers it
becomes as JSON on stdout (`{"project", "projectDir", "containers":
[<spec>...]}`), exit 1 and the refusal body above on stderr, exit 2 on a usage
error. Without `FILE` it reads the first of `compose.yaml`, `compose.yml`,
`docker-compose.yaml` and `docker-compose.yml` in the working directory; `-`
is stdin. The project directory is the file's directory, unless
`--project-dir` names one; for stdin it is `/mica/volumes/<project>/`, as the
daemon takes a project declared without one. Host paths, devices and
environment files are looked at on this host. It checks the file alone:
a container name another project's container or a container run by hand
holds is refused when the project is declared.

### On the device: the CLI

micad (mica-core) owns the projects: they are its settings, it declares them
as one set (`PUT /v1/projects`), and its console, API and device CLI change
that one set. On the device, as root over SSH (a key
in `access.ssh`), the CLI is `micad ctr` (or `mica-ctr`); see mica-core's
`docs/mica-core.md`, *Containers*:

```sh
mkdir -p /mica/compose/app && cd /mica/compose/app
cat >compose.yaml <<'YAML'
services:
  web:
    image: docker.io/library/nginx:1
    ports: ["8080:80"]
    volumes: ["./html:/usr/share/nginx/html:ro"]
    restart: unless-stopped
YAML
micad ctr up                      # project app (the directory's name), ./html is /mica/compose/app/html: app-web-1 runs
micad ctr list                    # every project's containers and their phase
micad ctr logs app/web --tail 50
micad ctr down                    # its containers and network go; ./html stays
```

`micad ctr up [-p PROJECT] [-f FILE]` and `micad ctr down [-p PROJECT]` find
and name the project as `ctl up` does.

- A project is stored by micad in `/mica/config/container/<project>.yaml`
  and declared to the daemon, which keeps it in
  `/var/lib/mica/containerd/projects/`; its volumes are under
  `/mica/volumes/<project>/`. All are on DATA, so they survive a reboot and an
  update or rollback of the system slot. Each service starts at every boot
  while containers are on.
- The console's containers switch (`container.enabled`) starts the daemon;
  turned off, micad stops every project's container, then stops and disables
  the daemon. The projects stay declared and start again when it is turned on.
- micad adopts, once, the projects put with `mica-containerd ctl up` that
  its settings lack. From then on its set is what the daemon runs: a
  project put or deleted with `ctl` is undone by micad's next declaration.

`mica-containerd ctl` is the daemon's own client, the low-level and debugging
path: `ctl up [-p PROJECT] [-f FILE] [--project-dir DIR]` and
`ctl down [-p PROJECT]` with docker compose's defaults (the file of the working
directory, the project named after its directory, which is its project
directory), `ctl list` (every container), `ctl get NAME`, `ctl logs --follow
NAME`, `ctl events`, `ctl status`, and `mica-containerd compose check`, which
needs no daemon. It reaches the
socket only while containers are on, and the socket is root's alone.

**Dependencies**: compose-go and what it needs are vendored under
`mica-containerd/vendor/` and built offline (`-mod=vendor`); `make upgrade`
moves them to their latest releases with the other pins.

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

- **Capabilities: podman's default set, changed only by `cap_add` and
  `cap_drop`.** A service that adds `ALL` or `CAP_SYS_ADMIN` is close to a
  privileged container; with `network_mode: host` and `CAP_NET_ADMIN` it
  changes the host's network.
- **Host paths and devices: what the file names.** A bind may name any host
  directory, `/` or `/etc` included, and `devices` hands the container the
  host's nodes; the API socket is root's alone, so whoever declares a project
  is root already.

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
MICA_ARCH=amd64 make smoke     # the archive installed and run in a privileged container (docker, network)
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
| `tests/podman-smoke.sh` | the archive installed in a privileged container and run: containers, networks and DNS, logs, health, capabilities, devices |
| `tools/release.sh` | publishes a release through `mica-tools` |
| `tools/base-check.sh` | the Debian dependencies against the pinned Base release |
| `tools/dev-pins.sh`, `pins/` | resolves, checks and fetches the build closure |
| `tools/upgrade.sh` | moves every pin to its latest release and bumps the package |
| `check-pins.sh` | which upstream tags have a newer release |
| `tools/buildx.sh`, `tools/build-cache.sh` | the builders, and CI's engine compile caches |
| `bin/mica-tools`, `locks/mica-build-tools.pin` | runs the pinned `mica-build-tools` |
| `tests/` | the offline tests; `tests/mica-tools-stub.sh` stands in for `bin/mica-tools` in their fixtures |
