package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/collective-projects/brm-server/pkg/models"

	"github.com/google/uuid"
)

// MoveStorage is an interface for storage backends that support moving artifacts.
// All ArtifactStorage implementations are required to implement this.
type MoveStorage interface {
	Move(ctx context.Context, srcHash, destHash string) error
}

// HashComputingArtifactStorage wraps an ArtifactStorage implementation to automatically
// compute SHA-256 hashes when the hash is unknown (empty, length<3, or "UNKNOWN").
type HashComputingArtifactStorage struct {
	storage models.ArtifactStorage
	logger  *slog.Logger
}

// NewHashComputingArtifactStorage creates a new HashComputingArtifactStorage wrapper.
func NewHashComputingArtifactStorage(storage models.ArtifactStorage) *HashComputingArtifactStorage {
	if storage == nil {
		panic("storage cannot be nil")
	}
	return &HashComputingArtifactStorage{
		storage: storage,
		logger:  slog.Default().With("component", "hash-computing-storage"),
	}
}

// GetStorageInfo returns information about the storage by delegating to the wrapped storage.
func (h *HashComputingArtifactStorage) GetStorageInfo() models.ArtifactStorageInfo {
	return h.storage.GetStorageInfo()
}

// Move renames an artifact from srcHash to destHash by delegating to the wrapped storage,
// which must implement MoveStorage. This lets HashComputingArtifactStorage itself satisfy
// MoveStorage, as required of every ArtifactStorage implementation.
func (h *HashComputingArtifactStorage) Move(ctx context.Context, srcHash, destHash string) error {
	moveStorage, ok := h.storage.(MoveStorage)
	if !ok {
		return fmt.Errorf("underlying storage does not implement Move method")
	}
	return moveStorage.Move(ctx, srcHash, destHash)
}

// isUnknownHash checks if the hash should be treated as unknown.
// Returns true if hash is empty, length < 3, or equals "UNKNOWN" (case-insensitive).
// Note: empty hash already satisfies length < 3, so we check it first.
func (h *HashComputingArtifactStorage) isUnknownHash(hash string) bool {
	if len(hash) < 3 {
		return true
	}
	return strings.EqualFold(hash, "UNKNOWN")
}

// generateTempHash generates a temporary UUID-based hash for initial storage.
func (h *HashComputingArtifactStorage) generateTempHash() string {
	id := uuid.New()
	return "temp-" + id.String()
}

// cleanupTempHash removes a temporary artifact.
func (h *HashComputingArtifactStorage) cleanupTempHash(ctx context.Context, tempHash string) error {
	// Check if temp artifact exists
	tempID := models.ArtifactIdentifier{Hash: tempHash}
	_, err := h.storage.GetMeta(ctx, tempID)
	if err != nil {
		// Already doesn't exist or error reading - nothing to clean up
		return nil
	}

	// Delete the artifact (this will delete all references and move to trash)
	err = h.storage.DeleteArtifact(ctx, tempID)
	return err
}

// handleExistingHash handles the case where the computed hash already exists.
// It cleans up the temp file and returns the existing metadata.
func (h *HashComputingArtifactStorage) handleExistingHash(
	ctx context.Context,
	computedHash string,
	tempHash string,
	tempMeta *models.ArtifactMeta,
	meta *models.ArtifactMeta,
) (*models.ArtifactMeta, error) {
	// Cleanup temp file; log but continue on failure (the temp artifact is orphaned in
	// trash rather than lost, so a failed cleanup here isn't fatal to the caller).
	if cleanupErr := h.cleanupTempHash(ctx, tempHash); cleanupErr != nil {
		h.logger.Warn("failed to clean up temporary artifact after hash already existed",
			"tempHash", tempHash, "computedHash", computedHash, "error", cleanupErr)
	}

	// Get existing metadata
	computedID := models.ArtifactIdentifier{Hash: computedHash}
	existingMeta, err := h.storage.GetMeta(ctx, computedID)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing metadata: %w", err)
	}

	// The metadata file might have the wrong hash if it was created via hash computing
	// and never corrected. Always ensure the returned metadata has the correct hash.
	existingMeta.Hash = computedHash

	return existingMeta, nil
}

// moveToFinalHash moves the artifact from temp hash to final computed hash.
// If the move fails because the hash already exists (race condition), it handles it gracefully.
func (h *HashComputingArtifactStorage) moveToFinalHash(
	ctx context.Context,
	tempHash string,
	computedHash string,
	tempMeta *models.ArtifactMeta,
	meta *models.ArtifactMeta,
) (*models.ArtifactMeta, error) {
	// All ArtifactStorage implementations must support Move
	moveStorage, ok := h.storage.(MoveStorage)
	if !ok {
		return nil, fmt.Errorf("storage does not implement Move method")
	}

	// Attempt to move from temp to final location
	if err := moveStorage.Move(ctx, tempHash, computedHash); err != nil {
		// Move failed - check if hash now exists (another goroutine might have created it)
		computedID := models.ArtifactIdentifier{Hash: computedHash}
		existingMeta, checkErr := h.storage.GetMeta(ctx, computedID)
		if checkErr == nil && existingMeta != nil {
			// Hash exists now (race condition: another goroutine created it)
			// Handle it as existing hash
			return h.handleExistingHash(ctx, computedHash, tempHash, tempMeta, meta)
		}
		// Move failed for other reason, cleanup and return error
		h.cleanupTempHash(ctx, tempHash)
		return nil, fmt.Errorf("failed to move from temp to final hash: %w", err)
	}

	// Metadata file has been moved. Re-read it from the new location and correct the hash field.
	computedID := models.ArtifactIdentifier{Hash: computedHash}
	finalMeta, err := h.storage.GetMeta(ctx, computedID)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata after move: %w", err)
	}

	// The metadata file still contains the temp hash in its Hash field
	// Correct it to the computed hash before returning
	finalMeta.Hash = computedHash

	return finalMeta, nil
}

// CreateArtifact streams data from 'r' to storage.
// If hash is unknown (empty, len<3, or "UNKNOWN"), computes SHA-256 hash automatically.
func (h *HashComputingArtifactStorage) CreateArtifact(ctx context.Context, id models.ArtifactIdentifier, r io.Reader, size int64, meta *models.ArtifactMeta) (*models.ArtifactMeta, error) {
	// Only handle hash-based identifiers (references already have hash in meta)
	if id.HasReference() && !id.HasHash() {
		// Delegate to underlying storage
		return h.storage.CreateArtifact(ctx, id, r, size, meta)
	}

	hash := id.Hash
	// Check if hash is unknown
	if !h.isUnknownHash(hash) {
		// Known hash: delegate directly to underlying storage
		return h.storage.CreateArtifact(ctx, id, r, size, meta)
	}

	// Unknown hash: compute it using temp storage approach
	tempHash := h.generateTempHash()

	// Create SHA-256 hasher
	hasher := sha256.New()

	// Use TeeReader to compute hash while streaming to storage
	teeReader := io.TeeReader(r, hasher)

	// Create artifact with temp hash (this streams the data)
	tempID := models.ArtifactIdentifier{Hash: tempHash}
	tempMeta, err := h.storage.CreateArtifact(ctx, tempID, teeReader, size, meta)
	if err != nil {
		return nil, fmt.Errorf("failed to create with temp hash: %w", err)
	}

	// Compute final hash from hasher
	computedHash := hex.EncodeToString(hasher.Sum(nil))

	// Check if computed hash already exists
	computedID := models.ArtifactIdentifier{Hash: computedHash}
	existingMeta, err := h.storage.GetMeta(ctx, computedID)
	if err == nil && existingMeta != nil {
		// Hash already exists: cleanup temp file and merge references
		return h.handleExistingHash(ctx, computedHash, tempHash, tempMeta, meta)
	}

	// Hash doesn't exist: move from temp to final location
	return h.moveToFinalHash(ctx, tempHash, computedHash, tempMeta, meta)
}

// ReadBlob returns a stream for the requested data.
func (h *HashComputingArtifactStorage) ReadBlob(ctx context.Context, req models.ArtifactRange) (io.ReadCloser, models.ArtifactRange, error) {
	return h.storage.ReadBlob(ctx, req)
}

// UpdateBlob modifies a specific range by streaming data from 'r'.
func (h *HashComputingArtifactStorage) UpdateBlob(ctx context.Context, req models.ArtifactRange, r io.Reader) error {
	return h.storage.UpdateBlob(ctx, req, r)
}

// DeleteArtifact removes a specific reference to an artifact.
func (h *HashComputingArtifactStorage) DeleteArtifact(ctx context.Context, id models.ArtifactIdentifier) error {
	return h.storage.DeleteArtifact(ctx, id)
}

// GetMeta reads the metadata JSON file.
func (h *HashComputingArtifactStorage) GetMeta(ctx context.Context, id models.ArtifactIdentifier) (*models.ArtifactMeta, error) {
	return h.storage.GetMeta(ctx, id)
}

// GetReference retrieves a specific reference by name and registry for an artifact.
func (h *HashComputingArtifactStorage) GetReference(ctx context.Context, hash, name, registry string) (*models.ArtifactReference, error) {
	return h.storage.GetReference(ctx, hash, name, registry)
}

// UpdateReference updates an existing reference.
func (h *HashComputingArtifactStorage) UpdateReference(ctx context.Context, hash string, ref models.ArtifactReference) (*models.ArtifactReference, error) {
	return h.storage.UpdateReference(ctx, hash, ref)
}

// ListReferenceHashes returns all reference hashes for an artifact.
func (h *HashComputingArtifactStorage) ListReferenceHashes(ctx context.Context, hash string) ([]string, error) {
	return h.storage.ListReferenceHashes(ctx, hash)
}
