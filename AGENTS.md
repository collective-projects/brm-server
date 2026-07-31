# AGENTS.md — brm-server

Binary Registry Manager (BRM) server: a self-hosted, content-addressable artifact
registry. Currently implements a Docker/OCI-compatible private registry over a
pluggable local blob storage layer. Early-stage (~6 months old); no auth, single
registry type fully implemented.

## Workspace layout

This repo is one module in a Go workspace (`../go.work`) alongside
`brm-config` (hierarchical YAML+env config library). Always run `go` commands
from within the workspace so the `replace` in `go.mod` resolves correctly.
`brm-config` is an internal sibling repo — per `.cursor/rules/go-standards-brm.mdc`,
don't modify it without calling it out and getting confirmation first.

```
brm-server/
  main.go                          entrypoint: load config, init storage+registries, serve, graceful shutdown
  pkg/models/                      core interfaces & types: ArtifactStorage, Registry, ArtifactIdentifier/Meta/Reference
  pkg/configkeys/                  string constants for YAML config keys/classes
  internal/storage/                ArtifactStorage implementations + StorageManager (factory/registry by "class" name)
  internal/registry/               RegistryManager + Docker/OCI registry implementations
  internal/registry/docker/        shared OCI types, manifest parsing, error responses
  internal/registry/docker/private/  private registry: HTTP handlers + service (manifests, blobs, uploads)
  internal/registry/docker/proxy/  proxy registry (upstream caching) — stub only, SetupRoutes unimplemented
  configs/application.yaml         default config (storage + registry instances)
```

## Architecture

- **Storage** is content-addressable by SHA-256 hash, decorator-composed:
  `SimpleFileStorage` (blob/meta/ref files on disk) → wrapped by
  `ConcurrentArtifactStorage` (per-hash file locks via `flock`) → wrapped by
  `HashComputingArtifactStorage` (computes hash from stream when caller doesn't
  know it yet, via temp-file-then-rename). All three implement
  `models.ArtifactStorage`; compose only in that order.
- **References** (name+registry → hash) are separate from blob storage, stored
  under `ref/` and `metaref/`, so one blob can be referenced by multiple tags.
- **Registries** and **storages** are both created through a class-name +
  params factory pattern (`StorageManager`/`RegistryManager`, singletons via
  `GetManager()`), configured declaratively from `configs/application.yaml`.
- Each registry instance owns its own `http.Server` bound to its configured
  `serviceBinding`; `RegistryManager.StartAllServers` starts them all and
  coordinates graceful shutdown.

See `TODOs.md` for the prioritized backlog and `README.md` for the intended
subsystem breakdown (storage/eventing/indexing/config/scheduler/serving) —
only storage + Docker private registry are implemented today.

## Conventions (from `.cursor/rules/go-standards-brm.mdc`)

- Imports under `github.com/collective-projects/` are internal, same workspace.
- No TODOs/dev hacks in shipped code; wrap errors with `%w`; never swallow errors.
- Prefer streaming I/O (`io.Reader`/`io.Writer`/`io.Copy`) over buffering full
  content in memory.
- Accept interfaces, return concrete types; avoid interface-per-struct.
- Keep the import graph acyclic.
- Modify existing `_test.go` files rather than adding new ones unless asked.

## Build & test

```
go build ./...
go vet ./...
go test ./...                 # from brm-server, inside the go.work workspace
./build/build_and_run.sh       # build + run
./build/start-limited.sh       # run with constrained CPU/memory (perf testing)
```

Config is loaded from `./configs` by default (override with
`APPLICATION_CONFIGURATION_DIR`); env vars override file config (prefix via
`APPLICATION_CONFIGURATION_PREFIX`, profiles via `APPLICATION_PROFILES_ACTIVE`).

Known flaky test: `TestHashComputingArtifactStorageConcurrentUnknownHashes`
(internal/storage) occasionally fails on Windows with "Access is denied" on
concurrent rename — see PLAN.md.

## Where to look for open work

`TODOs.md` (HighPri/MidPri/LowPri backlog) and `PLAN.md` (architecture/code
review recommendations, not yet implemented) are the current source of truth
for planned work — check both before starting something new.
