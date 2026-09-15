# 20260914-1932-oci-pool-release Publish the OCI pool and podman-pkgs.lock with each release

- **status**: completed
- **createdAt**: 2026-09-14 19:32
- **approvedAt**: 2026-09-14 19:50 (user: the lock lists the package, one per architecture)
- **relatedTask**: 20260914-1932-oci-pool-release

## Context

- `release.yml` (on `release: published`) builds and gates the tag on both
  native runners, then `tools/release.sh <tag>` attaches
  `mica-podman_<version>_<arch>.deb` (`+` written `.`) and `SHA256SUMS` to the
  release and reads them back anonymously. Nothing goes to a registry;
  `ghcr.io/micaoss/mica-podman` does not exist yet.
- The workspace pattern for packages in a registry (mica-system-base
  `src/publish.ts`, mica-boards `tools/deb/publish.sh`):
  `ghcr.io/micaoss/<repository>:pool.<arch>.<YYYYMMDD-HHMM>`, an OCI image
  manifest with `artifactType: application/vnd.mica.pool`, the empty config,
  one layer per archive (`application/vnd.mica.deb`, titled
  `org.opencontainers.image.title` with the archive's file name), and the
  annotations `org.opencontainers.image.{version,revision,created,source}` and
  `mica.arch`. Base names its pools in `system-base.lock` as
  `POOL_MICA_SYSTEM_BASE_<ARCH>=ghcr.io/...:pool.<arch>.<tag>@sha256:...`, and
  mica-build (`tools/system-base.sh`, `tools/pool.sh` on `released-inputs`)
  reads such keys and finds a package by its layer title.
- GHCR creates a new package private and has no visibility API: the first
  publish needs the user to make `mica-podman` public in the package settings.

## Proposal

1. `tools/release.sh <YYYYMMDD-HHMM>` itself publishes the pools (bash, curl
   and jq, no new tool), run by `release.yml` with `GITHUB_TOKEN`
   (`packages: write`), so the pools share its refusals before any write: a UTC
   tag, a clean HEAD on origin/main that the tag names, the published release,
   one `mica-podman` archive per architecture stamped with HEAD and one version;
   - per architecture, pushes `ghcr.io/micaoss/mica-podman:pool.<arch>.<tag>`
     in the format above with one layer, the archive under its real name
     (`mica-podman_<version>_<arch>.deb`). `created` is the commit time, so a
     rerun produces the same manifest digest;
   - a tag that already exists must be exactly that manifest (same digest),
     otherwise it is refused and nothing is replaced;
   - reads the manifest and the blob back with no credential and compares by
     sha256 (so a private package fails here with a message telling the user
     to make it public and rerun the job);
   - writes `podman-pkgs.lock`, one tab-separated row per package and
     architecture (user decision: the lock lists the package; there is one):
     ```
     # podman-pkgs.lock: mica-podman <tag>, generated when the release was published: package, architecture, version, sha256, pool (the archive is its layer with that sha256).
     mica-podman	amd64	<version>	<sha256>	ghcr.io/micaoss/mica-podman:pool.amd64.<tag>@sha256:<manifest>
     mica-podman	arm64	<version>	<sha256>	ghcr.io/micaoss/mica-podman:pool.arm64.<tag>@sha256:<manifest>
     ```
2. The release carries `podman-pkgs.lock` next to the two archives;
   `SHA256SUMS` covers all three. The pools are pushed and read back before any
   asset is uploaded, so a release carries a lock only after the pools it names
   read back anonymously.
3. Tests: `tests/release-test.sh` runs against a real registry, `registry:3`
   (distribution v3.1.1, pinned by digest) as a labelled sibling container
   reached on 127.0.0.1 (runner) or by name on the `traefik` network (the
   workspace container), and covers the pools, the lock, an idempotent rerun, a
   tag holding another manifest, and no registry write on every refusal.
4. README (CI and releases), `docs/plan` item, changelog.

## Risks

- First release: the anonymous read-back fails until the user makes the GHCR
  package `mica-podman` public; the job is rerun after that and is idempotent.
- Release assets change: consumers that list assets see `podman-pkgs.lock` and
  a `SHA256SUMS` with three lines.
- `make check` needs docker for the registry test (the CI check job has it).

## Scope

- In: `tools/release.sh`, `.github/workflows/release.yml`, `tests/release-test.sh`,
  `Makefile`, README, records.
- Out: cutting a release; changes to the archive itself; a rootfs or image
  artifact (mica-podman ships a package only); mica-build's consumption.

## Alternatives

- `POOL_MICA_PODMAN_<ARCH>` keys as in `system-base.lock` instead of rows:
  proposed, the user chose rows naming the package.
- Pushing with `oras` from a pinned image: one more pinned tool for four HTTP
  calls per architecture.
