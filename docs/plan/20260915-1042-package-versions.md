# 20260915-1042-package-versions Packages locked by their own version

- **status**: in_progress
- **createdAt**: 2026-09-15 10:42
- **approvedAt**: 2026-09-15 (user decision "全部按建议处理", dispatched by coordinator uj991oa2)
- **relatedTask**: 20260915-1042-package-versions

## Context

- Today the version is `<podman tag>+git<commit12>[.dirty]-1`, the archive
  carries Mica-Source-Commit and the HEAD commit time as mtime, pool manifests
  carry the release and commit, and every release rebuilds.

## Proposal

1. `deb/mica-podman.control` declares `Version: 5.8.6-1` and
   `X-Mica-Source-Date-Epoch: 1786640584` (template-only, stripped by
   `deb/pack.sh`); `tools/version.sh` reads both and requires the upstream
   part to be the podman tag. The engine build and the pack use that epoch;
   the stamp records it.
2. Mica-Source-Commit is dropped; the package gate and `tools/release.sh`
   require the declared version and no commit field.
3. `tools/package-inputs.sh <arch>`: sha256 over the sorted manifest of
   locks/upstream.lock, the Dockerfile, build.sh, the pack and stamp tools,
   `deb/`, `overlay/` and the architecture; build-env images excluded.
4. `tools/reuse.sh` (ci.yml read-only, release): against the latest release
   carrying the lock, a lower version is refused, a higher one built, the same
   version must carry the same inputs and the same bytes; a previous release
   without recorded inputs is no reuse source (D1 proposal).
5. Pool manifests carry only mica.source-repo and mica.arch, each layer its
   title and mica.inputs, so an unchanged pool keeps its digest.
6. `make offline` warns that no release was compared.

## Risks

- The first release under the rules has a lower dpkg version than the +git
  release before it (D1).
- netavark, aardvark-dns and podman embed the declared epoch instead of their
  own commit times: one-time byte change.

## Scope

- In: deb/, tools/, tests/, Dockerfile, build.sh, workflows, README, records.
