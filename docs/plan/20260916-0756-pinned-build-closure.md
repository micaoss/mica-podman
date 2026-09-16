# 20260916-0756-pinned-build-closure No consumer-time apt in the build

- **status**: in_progress
- **createdAt**: 2026-09-16 07:56
- **approvedAt**: 2026-09-16 07:56 (coordinator uj991oa2, decided with mica-system-base and mica-build-env)
- **relatedTask**: 20260916-0756-pinned-build-closure

## Context

- The c, rust and go stages ran `apt-get install` against whatever archive the
  build-env image pointed at, on every engine build; the bytes are compiled into
  the shipped binaries.
- Measured against the images: 33, 3 and 8 archives, 15.0 MB on amd64, far below
  the 62 packages / 117 MB estimated without subtracting what the images ship.

## Proposal

1. `pins/<stage>.roots` declares each stage's packages; `tools/dev-pins.sh
   resolve` resolves their closure against the stage image's own dpkg status
   (copied out with `docker create`, so a foreign architecture needs no
   emulation), writes one `source` row per archive in `locks/upstream.lock`
   and the install order in `pins/<stage>.<arch>`.
2. The Dockerfile installs those archives with `dpkg --install` from the
   `pins` build context; `build.sh` fetches and verifies them by sha256 first.
3. Staleness gate: `pins/resolved-for` records the build-env release, the three
   image references and the snapshot the closure was resolved against;
   `tools/dev-pins.sh check` refuses a build when the tree names others, and
   `build.sh` runs it before building.
4. `tests/dev-pins-test.sh` covers the gate, the sha list and the fetch.

## Risks

- The closure belongs to one build-env release; the gate makes that loud.
- The snapshot cannot be the Base apt row's: the build-env images carry newer
  packages, so apt resolves downgrades against them.

## Scope

- In: pins/, tools/dev-pins.sh, Dockerfile, build.sh, tools/package-inputs.sh,
  tests/, Makefile, README, records, locks/.
