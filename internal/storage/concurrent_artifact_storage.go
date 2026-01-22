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

// ConcurrentArtifactStorage wraps an ArtifactStorage implementation with file-based locking
// to ensure thread-safe and process-safe concurrent operations.
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

// acquireLock acquires a file lock for the given hash with timeout support.
// It respects the context deadline if set, otherwise uses the configured lockTimeout.
func (c *ConcurrentArtifactStorage) acquireLock(ctx context.Context, hash string) (*flock.Flock, error) {
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
	locked, err := fileLock.TryLockContext(lockCtx, retryDelay)
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

	fileLock, err := c.acquireLock(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.CreateArtifact(ctx, id, r, size, meta)
}

// ReadBlob returns a stream for the requested data.
// ReadBlob operations don't require locking as they're read-only.
func (c *ConcurrentArtifactStorage) ReadBlob(ctx context.Context, req models.ArtifactRange) (io.ReadCloser, models.ArtifactRange, error) {
	return c.storage.ReadBlob(ctx, req)
}

// UpdateBlob modifies a specific range by streaming data from 'r'.
// UpdateBlob operations don't require locking as they modify data, not metadata structure.
func (c *ConcurrentArtifactStorage) UpdateBlob(ctx context.Context, req models.ArtifactRange, r io.Reader) error {
	return c.storage.UpdateBlob(ctx, req, r)
}

// DeleteArtifact removes a specific reference to an artifact with locking.
// If id contains a reference, deletes that reference. If id only contains a hash, deletes all references.
// If no references remain, the artifact is moved to trash and nil is returned.
// If references remain, only the metadata is updated and the updated metadata is returned.
func (c *ConcurrentArtifactStorage) DeleteArtifact(ctx context.Context, id models.ArtifactIdentifier) (*models.ArtifactMeta, error) {
	// Resolve identifier to hash for locking
	var hash string
	if id.HasHash() {
		hash = id.Hash
	} else if id.HasReference() {
		// Need to resolve reference first to get hash
		// For now, delegate to storage which will handle resolution
		return c.storage.DeleteArtifact(ctx, id)
	}

	fileLock, err := c.acquireLock(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.DeleteArtifact(ctx, id)
}

// GetMeta reads the metadata JSON file.
// Read operations don't require locking as they're read-only.
func (c *ConcurrentArtifactStorage) GetMeta(ctx context.Context, id models.ArtifactIdentifier) (*models.ArtifactMeta, error) {
	return c.storage.GetMeta(ctx, id)
}

// UpdateMeta overwrites the metadata JSON file with locking.
func (c *ConcurrentArtifactStorage) UpdateMeta(ctx context.Context, meta models.ArtifactMeta) (*models.ArtifactMeta, error) {
	fileLock, err := c.acquireLock(ctx, meta.Hash)
	if err != nil {
		return nil, err
	}
	defer fileLock.Unlock()

	return c.storage.UpdateMeta(ctx, meta)
}

// Move renames an artifact and its metadata to a new hash location with locking.
// Locks the destination hash to prevent concurrent operations.
func (c *ConcurrentArtifactStorage) Move(ctx context.Context, srcHash, destHash string) error {
	// Lock the destination hash (where we're moving to)
	fileLock, err := c.acquireLock(ctx, destHash)
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
