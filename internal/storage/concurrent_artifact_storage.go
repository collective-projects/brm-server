package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/collective-projects/brm-server/pkg/models"

	"github.com/gofrs/flock"
)

// IdentifierResolver is implemented by storage backends that can resolve an
// ArtifactIdentifier carrying only a reference (no hash) to its canonical hash.
// ConcurrentArtifactStorage uses this to determine the correct lock key for
// reference-only identifiers passed to read operations.
type IdentifierResolver interface {
	ResolveIdentifier(ctx context.Context, id models.ArtifactIdentifier) (string, error)
}

// ConcurrentArtifactStorage wraps an ArtifactStorage implementation with file-based locking
// to ensure thread-safe and process-safe concurrent operations.
//
// Locking is reader/writer: operations that only observe state (ReadBlob, GetMeta,
// GetReference, ListReferenceHashes) take a shared lock, so concurrent reads of the same
// hash never block each other. Operations that mutate state (CreateArtifact, UpdateBlob,
// DeleteArtifact, UpdateReference, Move) take an exclusive lock, so a write is never
// interleaved with a concurrent read or another write of the same hash.
type ConcurrentArtifactStorage struct {
	storage     models.ArtifactStorage // Wrapped storage implementation
	lockDir     string                 // Directory for lock files
	lockTimeout time.Duration          // Timeout for lock acquisition
}

// NewConcurrentArtifactStorage creates a new ConcurrentArtifactStorage wrapper.
// Parameters:
//   - storage: The underlying ArtifactStorage implementation to wrap
//   - lockDir: Directory path where lock files will be stored
//   - lockTimeout: Maximum duration to wait for lock acquisition
func NewConcurrentArtifactStorage(
	storage models.ArtifactStorage,
	lockDir string,
	lockTimeout time.Duration,
) (*ConcurrentArtifactStorage, error) {
	if storage == nil {
		return nil, fmt.Errorf("storage cannot be nil")
	}
	if lockDir == "" {
		return nil, fmt.Errorf("lockDir cannot be empty")
	}
	if lockTimeout <= 0 {
		return nil, fmt.Errorf("lockTimeout must be positive")
	}

	// Ensure lock directory exists
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", err)
	}

	return &ConcurrentArtifactStorage{
		storage:     storage,
		lockDir:     lockDir,
		lockTimeout: lockTimeout,
	}, nil
}

// GetStorageInfo returns information about the storage by delegating to the wrapped storage.
func (c *ConcurrentArtifactStorage) GetStorageInfo() models.ArtifactStorageInfo {
	return c.storage.GetStorageInfo()
}

// GetLockPath returns the lock file path for a given hash using git-like structure.
// Exported for testing purposes.
func (c *ConcurrentArtifactStorage) GetLockPath(hash string) string {
	if len(hash) < 2 {
		return filepath.Join(c.lockDir, hash+".lock")
	}
	subDir := hash[:2]
	fileName := hash[2:] + ".lock"
	return filepath.Join(c.lockDir, subDir, fileName)
}

// resolveLockHash determines the hash to use as the lock key for the given identifier.
// If the identifier already carries a hash, it's used directly (no extra work). Otherwise,
// the wrapped storage must implement IdentifierResolver to resolve a reference-only
// identifier to its hash.
func (c *ConcurrentArtifactStorage) resolveLockHash(ctx context.Context, id models.ArtifactIdentifier) (string, error) {
	if id.HasHash() {
		return id.Hash, nil
	}
	resolver, ok := c.storage.(IdentifierResolver)
	if !ok {
		return "", fmt.Errorf("storage does not support resolving reference-only identifiers for locking")
	}
	return resolver.ResolveIdentifier(ctx, id)
}

// acquireLock acquires a file lock for the given hash with timeout support.
// exclusive selects a writer lock (blocks readers and other writers); when false, a
// shared/reader lock is taken instead, which does not block other concurrent readers.
// It respects the context deadline if set, otherwise uses the configured lockTimeout.
func (c *ConcurrentArtifactStorage) acquireLock(ctx context.Context, hash string, exclusive bool) (*flock.Flock, error) {
	// Build lock file path using git-like structure
	lockPath := c.GetLockPath(hash)

	// Ensure lock directory exists
	lockDir := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Create flock instance
	fileLock := flock.New(lockPath)

	// Use context with timeout (respects caller context, adds default if needed)
	lockCtx := ctx
	if _, hasTimeout := ctx.Deadline(); !hasTimeout {
		var cancel context.CancelFunc
		lockCtx, cancel = context.WithTimeout(ctx, c.lockTimeout)
		defer cancel()
	}

	// Acquire lock with retry (retry every 10ms)
	retryDelay := 10 * time.Millisecond
	var locked bool
	var err error
	if exclusive {
		locked, err = fileLock.TryLockContext(lockCtx, retryDelay)
	} else {
		locked, err = fileLock.TryRLockContext(lockCtx, retryDelay)
	}
	if err != nil {
		if err == context.DeadlineExceeded || err == context.Canceled {
			return nil, fmt.Errorf("lock acquisition timeout for hash %s after %v", hash, c.lockTimeout)
		}
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("lock acquisition timeout for hash %s after %v", hash, c.lockTimeout)
	}

	return fileLock, nil
}

// CreateArtifact streams data from 'r' to storage with locking.
// Returns the final metadata state (merged if artifact existed, new if created).
func (c *ConcurrentArtifactStorage) CreateArtifact(ctx context.Context, id models.ArtifactIdentifier, r io.Reader, size int64, meta *models.ArtifactMeta) (*models.ArtifactMeta, error) {
	// Resolve identifier to hash for locking
	var hash string
	if id.HasHash() {
		hash = id.Hash
	} else if id.HasReference() {
		// For reference-based creates, use hash from meta if available
		if meta != nil && meta.Hash != "" {
			hash = meta.Hash
		} else {
			// Can't lock without hash, delegate to storage which will handle it
			return c.storage.CreateArtifact(ctx, id, r, size, meta)
		}
	}

	fileLock, err := c.acquireLock(ctx, hash, true)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.CreateArtifact(ctx, id, r, size, meta)
}

// ReadBlob returns a stream for the requested data, holding a shared (reader) lock for the
// lifetime of the returned stream. Concurrent reads of the same hash proceed without blocking
// each other; a concurrent write to the same hash blocks until the read completes (rc.Close()).
func (c *ConcurrentArtifactStorage) ReadBlob(ctx context.Context, req models.ArtifactRange) (io.ReadCloser, models.ArtifactRange, error) {
	hash, err := c.resolveLockHash(ctx, req.Identifier)
	if err != nil {
		return nil, models.ArtifactRange{}, fmt.Errorf("failed to resolve identifier for locking: %w", err)
	}

	fileLock, err := c.acquireLock(ctx, hash, false)
	if err != nil {
		return nil, models.ArtifactRange{}, err
	}

	rc, actual, err := c.storage.ReadBlob(ctx, req)
	if err != nil {
		fileLock.Unlock()
		return nil, models.ArtifactRange{}, err
	}

	return &lockReleasingReadCloser{ReadCloser: rc, lock: fileLock}, actual, nil
}

// UpdateBlob modifies a specific range by streaming data from 'r', under an exclusive lock
// (it mutates blob content, so it must not run concurrently with reads or other writes).
func (c *ConcurrentArtifactStorage) UpdateBlob(ctx context.Context, req models.ArtifactRange, r io.Reader) error {
	hash, err := c.resolveLockHash(ctx, req.Identifier)
	if err != nil {
		return fmt.Errorf("failed to resolve identifier for locking: %w", err)
	}

	fileLock, err := c.acquireLock(ctx, hash, true)
	if err != nil {
		return err
	}
	defer fileLock.Unlock()

	return c.storage.UpdateBlob(ctx, req, r)
}

// DeleteArtifact removes a specific reference to an artifact, under an exclusive lock.
// If id contains a reference, deletes that reference. If id only contains a hash, deletes all references.
// If no references remain, the artifact is moved to trash.
func (c *ConcurrentArtifactStorage) DeleteArtifact(ctx context.Context, id models.ArtifactIdentifier) error {
	hash, err := c.resolveLockHash(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to resolve identifier for locking: %w", err)
	}

	fileLock, err := c.acquireLock(ctx, hash, true)
	if err != nil {
		return err
	}
	defer fileLock.Unlock()

	return c.storage.DeleteArtifact(ctx, id)
}

// GetMeta reads the metadata JSON file, holding a shared (reader) lock so concurrent reads
// don't block each other but are isolated from any in-progress write to the same hash.
func (c *ConcurrentArtifactStorage) GetMeta(ctx context.Context, id models.ArtifactIdentifier) (*models.ArtifactMeta, error) {
	hash, err := c.resolveLockHash(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve identifier for locking: %w", err)
	}

	fileLock, err := c.acquireLock(ctx, hash, false)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.GetMeta(ctx, id)
}

// GetReference retrieves a specific reference by name and registry for an artifact, under a
// shared (reader) lock.
func (c *ConcurrentArtifactStorage) GetReference(ctx context.Context, hash, name, registry string) (*models.ArtifactReference, error) {
	fileLock, err := c.acquireLock(ctx, hash, false)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.GetReference(ctx, hash, name, registry)
}

// UpdateReference updates an existing reference with an exclusive lock.
// Only Description and Tags can be modified.
func (c *ConcurrentArtifactStorage) UpdateReference(ctx context.Context, hash string, ref models.ArtifactReference) (*models.ArtifactReference, error) {
	fileLock, err := c.acquireLock(ctx, hash, true)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.UpdateReference(ctx, hash, ref)
}

// ListReferenceHashes returns all reference hashes for an artifact, under a shared
// (reader) lock.
func (c *ConcurrentArtifactStorage) ListReferenceHashes(ctx context.Context, hash string) ([]string, error) {
	fileLock, err := c.acquireLock(ctx, hash, false)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.ListReferenceHashes(ctx, hash)
}

// Move renames an artifact and its metadata to a new hash location with locking.
// Locks the destination hash to prevent concurrent operations.
func (c *ConcurrentArtifactStorage) Move(ctx context.Context, srcHash, destHash string) error {
	// Lock the destination hash (where we're moving to)
	fileLock, err := c.acquireLock(ctx, destHash, true)
	if err != nil {
		return err
	}
	defer fileLock.Unlock()

	// Delegate to underlying storage (which must implement MoveStorage)
	moveStorage, ok := c.storage.(MoveStorage)
	if !ok {
		return fmt.Errorf("underlying storage does not implement Move method")
	}

	return moveStorage.Move(ctx, srcHash, destHash)
}

// --- Helper for ReadBlob ---

// lockReleasingReadCloser wraps a blob's ReadCloser so the shared lock acquired for the read
// is held until the caller closes the stream, then released.
type lockReleasingReadCloser struct {
	io.ReadCloser
	lock *flock.Flock
}

func (r *lockReleasingReadCloser) Close() error {
	closeErr := r.ReadCloser.Close()
	unlockErr := r.lock.Unlock()
	if closeErr != nil {
		return closeErr
	}
	return unlockErr
}
