# PLAN.md — Architecture & Code Review Recommendations

Findings from a full-codebase review (2026-07-31). This is a prioritized backlog to work
through deliberately, separate from `TODOs.md` (feature backlog).

## Resolved (2026-07-31)

The following were implemented directly (not just recommended):

- **Blob digest now validated before commit** (was: critical #1). `PutBlob`
  (`internal/registry/docker/private/service.go`) now streams the upload to a temporary,
  unpublished storage key while hashing it; only if the computed SHA-256 matches the
  caller's claimed digest is it moved (`MoveStorage`) to its final content-addressed
  location and a reference created. On mismatch, the staged content is deleted and no
  reference is ever created. `HashComputingArtifactStorage` gained a `Move` method
  (delegating to the wrapped storage) so it satisfies the `MoveStorage` contract too,
  since this fix works regardless of which storage class is configured.
- **Digest algorithm is now validated** (was: critical #6). `PutBlob` rejects any digest
  that doesn't start with `sha256:` before treating it as a trustworthy storage key,
  instead of silently accepting any algorithm name and validating against a hardcoded
  SHA-256 assumption.
- **Upload paths no longer buffer whole blobs in memory** (was: critical #2).
  `UploadSession` now streams each `PATCH` chunk straight to a per-session temp file on
  disk (`os.CreateTemp`) instead of a growing `bytes.Buffer`; `CompleteBlobUpload` streams
  the temp file (+ final chunk, if any) via `io.MultiReader` straight into `PutBlob`
  without reading anything fully into memory. `handleSingleRequestBlobUpload`
  (`handlers.go`) no longer buffers the body via `io.ReadAll` when `Content-Length` is
  absent — it just passes `-1` through, which `PutBlob`/storage already handle.
- **Concurrent reads no longer contend with each other, only with writes** (was: critical
  #4). `ConcurrentArtifactStorage` (`internal/storage/concurrent_artifact_storage.go`) now
  takes a **shared** (reader) file lock for `ReadBlob`, `GetMeta`, `GetReference`, and
  `ListReferenceHashes`, and an **exclusive** (writer) lock for `CreateArtifact`,
  `UpdateBlob` (previously unlocked entirely, despite mutating data),
  `DeleteArtifact` (previously unlocked for reference-only deletes), `UpdateReference`,
  and `Move`. Multiple concurrent readers of the same hash now proceed without blocking
  each other; a write to a hash blocks until in-flight reads of that hash finish, and vice
  versa. Reference-only identifiers are resolved to a hash for locking via a new
  `IdentifierResolver` interface, implemented by `SimpleFileStorage.ResolveIdentifier`
  (exported alongside the existing private `resolveIdentifier`).
- **Swallowed cleanup error now logged** (was: critical #5).
  `HashComputingArtifactStorage.handleExistingHash` previously discarded the error from
  `cleanupTempHash` with only a comment claiming it was logged. It now has a real
  `*slog.Logger` and logs the failure with the temp/computed hash.

Not yet implemented (see `TODOs.md` HighPri): **no authentication or authorization on any
endpoint** (was: critical #3). Deliberately deferred — there's no access manager design
yet, and adding ad hoc checks now would likely need to be redone once one exists. Worth
discussing before doing any real design work here.

## High — reliability & test gaps

1. **Flaky test on Windows:**
   `TestHashComputingArtifactStorageConcurrentUnknownHashes`
   (`internal/storage/hash_computing_artifact_storage_test.go`) intermittently fails with
   `Access is denied` on `os.Rename` of the metadata file during concurrent creates of the
   same content. Worth root-causing before relying on `moveToFinalHash`'s race-handling
   under real concurrent load — Windows file-rename semantics (can't rename a file another
   handle has open without `FILE_SHARE_DELETE`) may be exposing a genuine race that
   doesn't surface the same way on Linux. Note this now also affects the new
   `publishStagedBlob` path in the registry service (same `Move`-based rename), so it's
   worth prioritizing.

2. **Upload sessions are process-local, in-memory state.**
   (`DockerRegistryPrivateService.uploadSessions`, plus each session's temp file on local
   disk). A server restart during a chunked upload drops it (client gets an opaque
   failure on the next `PATCH`/`PUT`) and leaks that session's temp file until the next
   process start (nothing cleans up temp files across restarts). Fine for a single-
   instance dev setup, but blocks horizontal scaling and crash-resilience — note this
   dependency before building around multi-instance deployment.

## Medium — design & maintainability

3. **Class/param dispatch is positional and stringly-typed.**
   `StorageManager`/`RegistryManager` factories
   (`internal/storage/manager.go`, `internal/registry/manager.go`) take
   `...interface{}` and type-assert parameters by position (e.g. storage factories expect
   `[alias, baseDir, lockDir, lockTimeout]` in that exact order). Adding or reordering a
   parameter is a silent runtime break with no compiler help. Consider a typed params
   struct per class (e.g. `ConcurrentFileStorageParams{BaseDir, LockDir, LockTimeout}`)
   decoded via `Config.Unmarshal`, passed as a single argument.

4. **Central type switches instead of interface methods.**
   `RegistryManager.SaveToConfig` and `StartAllServers`
   (`internal/registry/manager.go`) switch on concrete types
   (`*private.DockerRegistryPrivate`, `*proxy.DockerRegistryProxy`) to pull out
   `ServiceBinding`/`Alias`/config. Every new registry implementation (compound, raw, etc.
   — see `TODOs.md` HighPri) requires editing this shared function instead of just
   implementing an interface. Consider adding `GetServiceBinding() net.Addr`,
   `GetAlias() string`, and a config-export method to the `Registry` interface (or a small
   extra interface) so new implementations plug in without touching the manager.

5. **Proxy registry is a config-time-valid but runtime-broken stub.**
   `RegistryManager.init()` registers a working factory for `docker.registry.proxy` and
   `LoadFromConfig` will happily accept and construct one, but
   `DockerRegistryProxy.SetupRoutes` unconditionally returns `"Not Implemented !"` — the
   failure only surfaces when `StartAllServers` tries to wire routes, not at
   config-validation time. Either reject this class at config-load time until
   implemented, or fast-track the `TODOs.md` HighPri proxy work so the gap closes soon.

6. **No TLS option for registry HTTP servers.** `RegistryManager.StartAllServers` only
   ever calls `ListenAndServe` (plain HTTP). Real Docker clients require TLS or an
   explicit `insecure-registries` client-side opt-in; worth deciding the TLS story
   (terminate here vs. expect a reverse proxy in front) before this is used by any real
   Docker client.

7. **Manager singletons complicate testing/composability.**
   `storage.GetManager()` / `registry.GetManager()` are package-level `sync.Once`
   singletons holding global mutable maps. This works today but means tests share one
   global registry (class name collisions across parallel tests) and a process can only
   ever run one set of storages/registries. Not urgent, but worth keeping in mind if
   multi-tenant or in-process test parallelism becomes a goal — dependency injection
   (construct a manager, pass it explicitly) would be the natural fix.

## Low — polish

8. `ctx context.Context` is threaded through every storage method but never checked
   (`ctx.Err()`/`ctx.Done()`) during file I/O in `SimpleFileStorage` — long-running
   reads/writes can't currently be cancelled by a client disconnect or timeout even though
   the interface implies they can.
