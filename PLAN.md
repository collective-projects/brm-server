# PLAN.md — Architecture & Code Review Recommendations

Findings from a full-codebase review (2026-07-31, updated 2026-08-01). This is a
prioritized backlog to work through deliberately, separate from `TODOs.md` (feature
backlog).

Each item is labeled with a **Priority** (Critical/High/Medium/Low) and a **Category**
(the kind of problem it is), plus a **Scenario if not fixed** — what concretely happens
if the item is left alone. Storage-subsystem items (`internal/storage`, plus the
storage-facing parts of blob ingestion in `internal/registry/docker/private`) are grouped
together below; everything else follows in its own section.

## Storage Subsystem

Covers `internal/storage` (`SimpleFileStorage`, `ConcurrentArtifactStorage`,
`HashComputingArtifactStorage`) and the blob-ingestion path in
`internal/registry/docker/private/service.go` (`PutBlob`, `UploadSession`), since that
code implements storage write-path semantics on the registry's behalf. This is the
subsystem in active use (`hashcomputing.filestorage`), so it gets the most scrutiny.

### Resolved (2026-07-31)

- **Blob digest now validated before commit.**
  **Priority:** Critical (resolved) · **Category:** Security / Correctness
  `PutBlob` (`internal/registry/docker/private/service.go`) now streams the upload to a
  temporary, unpublished storage key while hashing it; only if the computed SHA-256
  matches the caller's claimed digest is it moved (`MoveStorage`) to its final
  content-addressed location and a reference created. On mismatch, the staged content is
  deleted and no reference is ever created. `HashComputingArtifactStorage` gained a `Move`
  method (delegating to the wrapped storage) so it satisfies the `MoveStorage` contract
  too, since this fix works regardless of which storage class is configured.
  **Scenario if left unfixed:** a client could persist arbitrary content under a
  self-chosen digest before validation failed, corrupting the content-addressable
  guarantee and wasting disk on unvalidated writes with no cleanup.

- **Digest algorithm is now validated.**
  **Priority:** Critical (resolved) · **Category:** Security
  `PutBlob` rejects any digest that doesn't start with `sha256:` before treating it as a
  trustworthy storage key, instead of silently accepting any algorithm name and validating
  against a hardcoded SHA-256 assumption.
  **Scenario if left unfixed:** a client claiming an unsupported/spoofed algorithm prefix
  would have its content keyed and later compared under an implicit, unenforced SHA-256
  assumption.

- **Upload paths no longer buffer whole blobs in memory.**
  **Priority:** Critical (resolved) · **Category:** Performance
  `UploadSession` now streams each `PATCH` chunk straight to a per-session temp file on
  disk (`os.CreateTemp`) instead of a growing `bytes.Buffer`; `CompleteBlobUpload` streams
  the temp file (+ final chunk, if any) via `io.MultiReader` straight into `PutBlob`
  without reading anything fully into memory. `handleSingleRequestBlobUpload`
  (`handlers.go`) no longer buffers the body via `io.ReadAll` when `Content-Length` is
  absent — it just passes `-1` through, which `PutBlob`/storage already handle.
  **Scenario if left unfixed:** every chunked or unsized upload of a multi-GB Docker layer
  would hold the whole blob in RAM, OOM-ing the process well before disk or network became
  the bottleneck.

- **Concurrent reads no longer contend with each other, only with writes.**
  **Priority:** Critical (resolved) · **Category:** Correctness (race condition)
  `ConcurrentArtifactStorage` (`internal/storage/concurrent_artifact_storage.go`) now
  takes a **shared** (reader) file lock for `ReadBlob`, `GetMeta`, `GetReference`, and
  `ListReferenceHashes`, and an **exclusive** (writer) lock for `CreateArtifact`,
  `UpdateBlob` (previously unlocked entirely, despite mutating data),
  `DeleteArtifact` (previously unlocked for reference-only deletes), `UpdateReference`,
  and `Move`. Multiple concurrent readers of the same hash now proceed without blocking
  each other; a write to a hash blocks until in-flight reads of that hash finish, and vice
  versa. Reference-only identifiers are resolved to a hash for locking via a new
  `IdentifierResolver` interface, implemented by `SimpleFileStorage.ResolveIdentifier`
  (exported alongside the existing private `resolveIdentifier`).
  **Scenario if left unfixed:** a reader could observe a partially-written blob or
  metadata file concurrently with an in-progress write, returning corrupt/truncated data
  to a client with no error.

- **Swallowed cleanup error now logged.**
  **Priority:** Critical (resolved) · **Category:** Observability / Correctness
  `HashComputingArtifactStorage.handleExistingHash` previously discarded the error from
  `cleanupTempHash` with only a comment claiming it was logged. It now has a real
  `*slog.Logger` and logs the failure with the temp/computed hash.
  **Scenario if left unfixed:** temp-artifact cleanup failures would go completely
  unnoticed, making orphaned temp-file buildup (see below) undiagnosable in production.

### Open — Performance (large data / hash-computing storage)

- **Dedup fast-path is broken for large, already-stored blobs (regression from the fix
  above).**
  **Priority:** High · **Category:** Performance
  `PutBlob` now always stages the upload under a fresh, never-before-seen `tempKey` before
  checking anything. `SimpleFileStorage.CreateArtifact` has a cheap `os.Stat`-based dedup
  check that skips writing the blob entirely when the target hash already exists — but
  that check can never fire for a brand-new `tempKey`, so every push now writes the full
  blob to disk first, then discovers via `Move` that it was a duplicate and deletes it.
  Docker registries lean heavily on shared base-layer reuse, so this costs a full disk
  write for every re-push of a common layer that used to be a cheap `stat`.
  **Recommendation:** check `GetMeta(storageKey)` before staging; if it already exists,
  hash the incoming stream into `io.Discard` (CPU-only, no disk I/O) to confirm the
  client's data really matches, then just create the reference — skip the write/move
  entirely.
  **Scenario if not fixed:** every image push re-writes every shared/base layer to disk in
  full, even though the registry already has it — disk and time cost scale with total
  bytes pushed instead of bytes of genuinely new content, which matters a lot once
  "mostly hash-computing storage, mostly large data" is the normal workload.

- **Chunked uploads are written to disk twice.**
  **Priority:** High · **Category:** Performance
  `UploadSession` streams `PATCH` chunks to a temp file in the OS temp dir; `
  CompleteBlobUpload` then re-reads that whole file through `PutBlob`, which writes it
  *again* to a storage-side `tempKey` before the final rename. For a multi-GB layer sent
  in chunks, that's two full writes before the data lands in its permanent location.
  **Recommendation:** have the storage-side temp artifact receive chunks directly
  (`CreateArtifact` once to create it, `UpdateBlob` per chunk to append), so there's
  exactly one on-disk copy before the atomic rename to the final hash.
  **Scenario if not fixed:** disk I/O and upload latency for chunked pushes of large
  layers is roughly double what it needs to be, and OS-temp-dir usage scales with
  in-flight chunked upload volume on a filesystem that may not be sized for it (see next
  item).

- **No fsync anywhere — durability gap.**
  **Priority:** Medium · **Category:** Durability
  Blob and metadata writes are never flushed (`os.Create`/`io.Copy`/`json.Encode` only). A
  crash right after a client receives `201 Created` can lose data it believes is durably
  stored. This is a genuine trade-off, not a free fix — fsync-per-blob would hurt
  large-data throughput, so it needs a deliberate choice (e.g. fsync metadata only,
  periodic/batched fsync, or an explicitly documented risk acceptance).
  **Recommendation:** don't fsync blob data itself (too costly for large layers); fsync
  only the small metadata file after write, since that's what makes an artifact
  "discoverable" — this bounds the exposure to "blob bytes on disk but not yet
  registered" rather than losing tracked, referenced artifacts. If that's still too
  costly at high ingestion rates, batch it (a periodic background flush of recently
  written directories) instead of skipping it silently. Whatever's chosen, document the
  durability guarantee explicitly (e.g. "acknowledged writes may be lost only within N
  seconds of a crash") so it's a stated trade-off, not an implicit gap.
  **Scenario if not fixed:** a server crash or power loss immediately after upload
  acknowledgment can silently lose blobs/metadata the client (and any manifest referencing
  them) believes are safely stored — surfaces later as an unexplained missing layer.

- **Nothing ever gets garbage-collected.**
  **Priority:** Medium · **Category:** Operability (disk growth)
  `.trash/` (populated by every delete, every digest-mismatch cleanup, and every
  duplicate-upload cleanup) and `.lock/` files (`ConcurrentArtifactStorage`, one per hash
  ever locked, never removed after unlock — only the OS lock is released) both accumulate
  forever. No compaction/GC job exists (matches the unimplemented "eventing/indexing"
  subsystems in `README.md`).
  **Recommendation:** add a background sweeper (natural fit for the planned "SCHEDULER
  SUBSYSTEM" in `README.md`) that periodically purges `.trash/` entries older than a
  configurable retention window (e.g. a new `storage.<alias>.params.trashRetention` config
  key), and removes `.lock/` files that aren't currently held (a lock file is safe to
  delete once it can itself be locked, i.e. nobody holds it). As a cheaper first step
  before building a full scheduler, a manual/admin-triggered GC operation (even a CLI flag
  on the server binary) would already stop unbounded growth.
  **Scenario if not fixed:** disk usage grows monotonically even for content that's been
  explicitly deleted or was never valid, and directories accumulate large numbers of small
  files over time (lock files: one per unique hash ever seen) — eventually a capacity or
  directory-listing-performance problem, worse the larger and more frequently churned the
  stored data is.

- **No Range/partial GET support for large blobs (adjacent — handler layer, not storage).**
  **Priority:** Low-Medium · **Category:** Feature gap / Performance
  `handleGetBlob` (`internal/registry/docker/private/handlers.go`) always requests
  `Offset:0, Length:-1` from `ReadBlob` regardless of the client's `Range` header, even
  though `ArtifactStorage.ReadBlob`/`SimpleFileStorage` already support arbitrary byte
  ranges. The gap is purely in the HTTP handler not forwarding `Range`.
  **Recommendation:** parse the `Range` request header in `handleGetBlob` (standard
  `bytes=start-end` form), translate it into `models.ByteRange{Offset, Length}`, and pass
  it through to `service.GetBlob`/`ReadBlob` — the storage layer already does the rest.
  Return `206 Partial Content` with a `Content-Range` header when a range is honored, and
  `416 Range Not Satisfiable` for an invalid one. This is low-effort precisely because the
  storage layer already supports it end-to-end; only handler wiring is missing.
  **Scenario if not fixed:** clients can't resume an interrupted pull of a large layer —
  every retry re-downloads the full blob from byte zero.

### Open — Reliability

- **Flaky test on Windows.**
  **Priority:** High · **Category:** Reliability / Correctness
  `TestHashComputingArtifactStorageConcurrentUnknownHashes`
  (`internal/storage/hash_computing_artifact_storage_test.go`) intermittently fails with
  `Access is denied` on `os.Rename` of the metadata file during concurrent creates of the
  same content. Windows file-rename semantics (can't rename a file another handle has open
  without `FILE_SHARE_DELETE`) may be exposing a genuine race that doesn't surface the same
  way on Linux. This now also affects the `publishStagedBlob` path in the registry service
  (same `Move`-based rename).
  **Recommendation:** root-cause before treating this as understood — reproduce reliably
  (loop the test, or add a stress variant with more goroutines/iterations), and confirm
  which rename fails (blob vs. metadata) and whether it's the source or destination handle
  still open. Our new shared/exclusive locking (resolved above) already prevents a reader
  from being mid-read during a write's exclusive lock, so the likely remaining culprit is
  something outside application-level locking holding a handle open (e.g. an unclosed
  `os.Open`/`os.Stat` result, or Windows Defender/indexing scanning the file). If renaming
  a file that may have external open handles must be supported, consider a
  Windows-specific open path that requests `FILE_SHARE_DELETE` (Go's `os.Open` doesn't ask
  for this by default), or a bounded rename-with-retry as a pragmatic mitigation in the
  meantime.
  **Scenario if not fixed:** concurrent pushes of identical new content (or concurrent
  duplicate-blob publishes, per the dedup item above) can intermittently fail in
  production on Windows deployments, not just in tests.

- **Upload sessions are process-local, in-memory state.**
  **Priority:** High · **Category:** Durability / Reliability
  `DockerRegistryPrivateService.uploadSessions`, plus each session's temp file on local
  disk, live only for the life of the process. A server restart during a chunked upload
  silently drops it (client gets an opaque failure on the next `PATCH`/`PUT`), and nothing
  cleans up that session's temp file across restarts. Fine for a single-instance dev
  setup, but blocks horizontal scaling and crash-resilience.
  **Recommendation:** don't over-engineer this ahead of need — for a single-instance
  deployment, explicitly documenting the constraint (as done here) is enough for now. If
  it needs solving: persist session metadata (UUID, name, offset, temp file path) to a
  small on-disk sidecar so a restart can rediscover in-flight sessions and either resume
  or safely discard them, and add a startup sweep that removes orphaned upload temp files
  left by a previous, uncleanly-terminated process (nothing does this today even for a
  plain crash). This also naturally improves once the "chunked uploads written twice" item
  above is fixed — if chunks are staged directly in storage rather than a separate OS-temp
  file, the storage layer itself becomes the durable record of in-flight bytes, and only
  small session metadata needs separate persistence.
  **Scenario if not fixed:** any deploy, crash, or restart that happens mid-upload loses
  the entire in-flight upload (however large) and leaks its temp file permanently; this
  can't scale past one server instance.

### Open — Polish

- **Context cancellation isn't respected.**
  **Priority:** Low · **Category:** Performance / Resource cleanup
  `ctx context.Context` is threaded through every storage method but never checked
  (`ctx.Err()`/`ctx.Done()`) during file I/O in `SimpleFileStorage` — long-running
  reads/writes can't currently be cancelled by a client disconnect or timeout even though
  the interface implies they can.
  **Recommendation:** wrap the `io.Copy`/`io.CopyN` calls in `SimpleFileStorage` with a
  small context-aware reader (checks `ctx.Err()` between chunks and returns early if
  cancelled) rather than passing the raw reader straight through — a bounded-size buffered
  copy loop that checks `ctx.Done()` each iteration is enough; no need for anything fancier.
  Low priority — worth doing once the large-data throughput items above land, since this
  one is about resource cleanup, not correctness.
  **Scenario if not fixed:** a client aborting a large upload or download doesn't stop the
  server-side read/write loop — server resources (goroutine, file handle, disk I/O) keep
  running to completion for a response nobody will receive.

## Registry / API (non-storage)

- **Central type switches instead of interface methods.**
  **Priority:** Medium · **Category:** Design / Maintainability
  `RegistryManager.SaveToConfig` and `StartAllServers`
  (`internal/registry/manager.go`) switch on concrete types
  (`*private.DockerRegistryPrivate`, `*proxy.DockerRegistryProxy`) to pull out
  `ServiceBinding`/`Alias`/config. Every new registry implementation (compound, raw, etc.
  — see `TODOs.md` HighPri) requires editing this shared function instead of just
  implementing an interface.
  **Recommendation:** add `GetServiceBinding() net.Addr`, `GetAlias() string`, and a
  config-export method to the `Registry` interface (or a small extra interface) so new
  implementations plug in without touching the manager.
  **Scenario if not fixed:** every new registry type (compound, raw, ...) silently needs a
  matching edit in two central functions, easy to forget and only caught at runtime.

- **Proxy registry is a config-time-valid but runtime-broken stub.**
  **Priority:** Medium · **Category:** Correctness / UX
  `RegistryManager.init()` registers a working factory for `docker.registry.proxy` and
  `LoadFromConfig` will happily accept and construct one, but
  `DockerRegistryProxy.SetupRoutes` unconditionally returns `"Not Implemented !"` — the
  failure only surfaces when `StartAllServers` tries to wire routes, not at
  config-validation time.
  **Recommendation:** reject this class at config-load time until implemented, or
  fast-track the `TODOs.md` HighPri proxy work so the gap closes soon.
  **Scenario if not fixed:** an operator configuring a proxy registry gets a confusing
  runtime-only failure deep in server startup instead of an immediate, clear config error.

- **No TLS option for registry HTTP servers.**
  **Priority:** Medium · **Category:** Security / Compatibility
  `RegistryManager.StartAllServers` only ever calls `ListenAndServe` (plain HTTP). Real
  Docker clients require TLS or an explicit `insecure-registries` client-side opt-in.
  **Recommendation:** decide the TLS model explicitly rather than leaving it implicit.
  Cheapest to ship: document that TLS termination happens in a reverse proxy in front of
  the registry, and keep the server itself HTTP-only by design — defers cert management
  entirely. If termination should happen here instead, add optional `tls` config (cert/key
  paths) per registry `serviceBinding` and use `http.Server.ServeTLS` when configured,
  falling back to plain HTTP when it isn't (opt-in, not a breaking default change).
  **Scenario if not fixed:** the registry can't be used by a standard Docker client
  without every client being reconfigured for insecure access, and traffic (including
  upstream credentials, once the proxy registry exists) is unencrypted.

## Cross-cutting / Design

- **Class/param dispatch is positional and stringly-typed.**
  **Priority:** Medium · **Category:** Design / Maintainability
  `StorageManager`/`RegistryManager` factories
  (`internal/storage/manager.go`, `internal/registry/manager.go`) take
  `...interface{}` and type-assert parameters by position (e.g. storage factories expect
  `[alias, baseDir, lockDir, lockTimeout]` in that exact order). Adding or reordering a
  parameter is a silent runtime break with no compiler help.
  **Recommendation:** a typed params struct per class (e.g.
  `ConcurrentFileStorageParams{BaseDir, LockDir, LockTimeout}`) decoded via
  `Config.Unmarshal`, passed as a single argument.
  **Scenario if not fixed:** a future refactor that adds/reorders a factory parameter
  compiles cleanly and fails (or silently misbehaves) only at runtime, for either
  managers.

- **Manager singletons complicate testing/composability.**
  **Priority:** Low · **Category:** Design / Testability
  `storage.GetManager()` / `registry.GetManager()` are package-level `sync.Once`
  singletons holding global mutable maps. Tests share one global registry (class name
  collisions across parallel tests) and a process can only ever run one set of
  storages/registries.
  **Recommendation:** dependency injection (construct a manager, pass it explicitly)
  instead of package-level singletons.
  **Scenario if not fixed:** parallel tests that pick colliding aliases flake against each
  other, and multi-tenant or multi-instance-per-process designs are blocked without a
  rework.
