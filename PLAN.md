# PLAN.md — Architecture & Code Review Recommendations

Findings from a full-codebase review (2026-07-31). Nothing here has been
implemented — this is a prioritized backlog to work through deliberately,
separate from `TODOs.md` (feature backlog). Each item names the file(s),
the problem, and a suggested direction; sizing/approach should be finalized
before starting.

## Critical — correctness & security

1. **Blob digest is validated after the data is already committed to storage.**
   `internal/registry/docker/private/service.go` `PutBlob`: `CreateArtifact`
   writes the blob under a storage key derived from the *client-supplied*
   digest first, and only *then* hashes the stream to check it matches. On
   mismatch, the function returns an error but never deletes the
   already-stored artifact — the registry ends up storing (and keeping)
   content under a hash that doesn't match its own storage key convention.
   This is both a content-integrity bug (content-addressable storage no
   longer guarantees hash == content) and lets a client fill disk with
   unvalidated data cheaply. `PutManifest` has the same shape (digest
   computed from the payload itself, so lower risk, but same "write-then-
   maybe-fail" flow with no cleanup). Fix direction: validate digest against
   the stream *before* the storage write commits (hash while streaming into
   a temp location, as `HashComputingArtifactStorage` already does — reuse
   that pattern instead of trusting the client's digest as the storage key),
   or delete-on-mismatch as a minimum fix.

2. **Upload paths buffer entire blobs in memory**, directly contradicting the
   project's own streaming-I/O standard
   (`.cursor/rules/go-standards-brm.mdc`):
   - `internal/registry/docker/private/service.go` `UploadSession.Data` is a
     `*bytes.Buffer` that accumulates every chunk of a chunked upload
     (`UploadBlobChunk`) in RAM for the life of the session.
   - `handleSingleRequestBlobUpload` and `CompleteBlobUpload`
     (same package: `handlers.go`, `service.go`) call `io.ReadAll` on request
     bodies when `Content-Length` is absent, or to size the final chunk.
   - Docker/OCI layers are routinely hundreds of MB to multiple GB; this
     will OOM the process under real usage and caps effective concurrency
     long before disk or network would. Fix direction: stream chunks
     directly to a per-session temp file (offset-addressed, `os.File` +
     `WriteAt` or sequential append) and only finalize via `Move`/rename,
     mirroring how `HashComputingArtifactStorage` already streams-with-
     temp-file for unknown hashes. Also add a `http.MaxBytesReader` /
     configurable upload size cap regardless.

3. **No authentication or authorization on any endpoint.** Every `/v2/*`
   route in `internal/registry/docker/private/handlers.go` is open — anyone
   with network access can push, pull, or overwrite any repository. Expected
   at this stage (the "SERVING SUBSYSTEM" in `README.md` is explicitly
   unbuilt), but flagging so it's a tracked, deliberate gap rather than a
   surprise before any real deployment — should block exposing this beyond a
   trusted network.

4. **Readers aren't isolated from in-progress writes.**
   `internal/storage/simple_file_storage.go` `CreateArtifact` writes directly
   to the final blob path (no write-to-temp-then-rename), and
   `internal/storage/concurrent_artifact_storage.go` only takes a lock for
   `CreateArtifact`/`DeleteArtifact`/`UpdateReference`/`Move` — `ReadBlob` and
   `UpdateBlob` are unlocked "because they're read-only", but that reasoning
   doesn't hold: a concurrent `ReadBlob` can observe a partially-written file
   while another goroutine's `CreateArtifact` is mid-`io.Copy`, since nothing
   about the create is atomic from the reader's point of view. Fix direction:
   write new blobs to a temp file in the same directory and `os.Rename` into
   place once complete (rename is atomic on the same filesystem), so a
   concurrent reader either sees the old state or the fully-written new
   state, never a partial write.

5. **Silently swallowed cleanup error.**
   `internal/storage/hash_computing_artifact_storage.go` `handleExistingHash`
   has a comment claiming `"Log cleanup error but continue"` but the branch
   body is empty — the error from `cleanupTempHash` is discarded with no
   logging at all, contradicting the "never swallow errors" standard. Small
   fix: actually log it (the type already has no logger — would need one
   threaded in, or use `slog.Default()`).

6. **Digest algorithm isn't validated.**
   `getStorageKey` (`service.go`) strips anything before the first `:` and
   uses the remainder as the storage key, regardless of what algorithm the
   client claimed — but `validateDigest`/`PutBlob` always compute and compare
   SHA-256. A client sending `sha1:...` or a bogus algorithm name would
   still be accepted/rejected based on an implicit SHA-256 assumption that
   isn't enforced against the stated algorithm. Should reject any digest
   that doesn't start with `sha256:` explicitly (or implement the other
   algorithms it claims to support).

## High — reliability & test gaps

7. **Flaky test on Windows:**
   `TestHashComputingArtifactStorageConcurrentUnknownHashes`
   (`internal/storage/hash_computing_artifact_storage_test.go`) intermittently
   fails with `Access is denied` on `os.Rename` of the metadata file during
   concurrent creates of the same content. This surfaced during this
   review's test run (passed on retry). Worth root-causing before relying on
   `moveToFinalHash`'s race-handling under real concurrent load — Windows
   file-rename semantics (can't rename an open/handle-held file) may be
   exposing a genuine race that just happens not to fail the same way on
   Linux.

8. **Upload sessions are process-local, in-memory state only**
   (`DockerRegistryPrivateService.uploadSessions`). A server restart during a
   chunked upload silently drops it (client gets an opaque failure on the
   next `PATCH`/`PUT`), and every session (plus its buffered bytes — see #2)
   lives only as long as the process. This is fine for a single-instance
   dev setup but blocks horizontal scaling and crash-resilience; note this
   dependency before building around multi-instance deployment.

## Medium — design & maintainability

9. **Class/param dispatch is positional and stringly-typed.**
   `StorageManager`/`RegistryManager` factories
   (`internal/storage/manager.go`, `internal/registry/manager.go`) take
   `...interface{}` and type-assert parameters by position (e.g. storage
   factories expect `[alias, baseDir, lockDir, lockTimeout]` in that exact
   order). Adding or reordering a parameter is a silent runtime break with
   no compiler help. Consider a typed params struct per class (e.g.
   `ConcurrentFileStorageParams{BaseDir, LockDir, LockTimeout}`) decoded via
   `Config.Unmarshal`, passed as a single argument.

10. **Central type switches instead of interface methods.**
    `RegistryManager.SaveToConfig` and `StartAllServers`
    (`internal/registry/manager.go`) switch on concrete types
    (`*private.DockerRegistryPrivate`, `*proxy.DockerRegistryProxy`) to pull
    out `ServiceBinding`/`Alias`/config. Every new registry implementation
    (compound, raw, etc. — see `TODOs.md` HighPri) requires editing this
    shared function instead of just implementing an interface. Consider
    adding `GetServiceBinding() net.Addr`, `GetAlias() string`, and a config-
    export method to the `Registry` interface (or a small extra interface)
    so new implementations plug in without touching the manager.

11. **Proxy registry is a config-time-valid but runtime-broken stub.**
    `RegistryManager.init()` registers a working factory for
    `docker.registry.proxy` and `LoadFromConfig` will happily accept and
    construct one, but `DockerRegistryProxy.SetupRoutes` unconditionally
    returns `"Not Implemented !"` — the failure only surfaces when
    `StartAllServers` tries to wire routes, not at config-validation time.
    Either reject this class at config-load time until implemented, or
    fast-track the TODOs.md HighPri proxy work so the gap closes soon.

12. **No TLS option for registry HTTP servers.** `RegistryManager.
    StartAllServers` only ever calls `ListenAndServe` (plain HTTP). Real
    Docker clients require TLS or an explicit `insecure-registries` client-
    side opt-in; worth deciding the TLS story (terminate here vs. expect a
    reverse proxy in front) before this is used by any real Docker client.

13. **Manager singletons complicate testing/composability.**
    `storage.GetManager()` / `registry.GetManager()` are package-level
    `sync.Once` singletons holding global mutable maps. This works today but
    means tests share one global registry (class name collisions across
    parallel tests) and a process can only ever run one set of storages/
    registries. Not urgent, but worth keeping in mind if multi-tenant or
    in-process test parallelism becomes a goal — dependency injection
    (construct a manager, pass it explicitly) would be the natural fix.

## Low — polish

14. `ctx context.Context` is threaded through every storage method but never
    checked (`ctx.Err()`/`ctx.Done()`) during file I/O in
    `SimpleFileStorage` — long-running reads/writes can't currently be
    cancelled by a client disconnect or timeout even though the interface
    implies they can.
