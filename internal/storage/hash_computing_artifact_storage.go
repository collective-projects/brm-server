package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

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
}

// NewHashComputingArtifactStorage creates a new HashComputingArtifactStorage wrapper.
func NewHashComputingArtifactStorage(storage models.ArtifactStorage) *HashComputingArtifactStorage {
	if storage == nil {
		panic("storage cannot be nil")
	}
	return &HashComputingArtifactStorage{
		storage: storage,
	}
}

// GetStorageInfo returns information about the storage by delegating to the wrapped storage.
func (h *HashComputingArtifactStorage) GetStorageInfo() models.ArtifactStorageInfo {
	return h.storage.GetStorageInfo()
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

// cleanupTempHash removes a temporary artifact by adding a cleanup reference and then deleting it.
func (h *HashComputingArtifactStorage) cleanupTempHash(ctx context.Context, tempHash string) error {
	// Check if temp artifact exists
	tempMeta, err := h.storage.GetMeta(ctx, tempHash)
	if err != nil {
		// Already doesn't exist or error reading - nothing to clean up
		return nil
	}

	// If no references exist, add a temporary one so we can delete it
	if len(tempMeta.References) == 0 {
		tempRef := models.ArtifactReference{
			Name:                "temp-cleanup",
			Repo:                "temp-cleanup",
			ReferencedTimestamp: time.Now().Unix(),
		}
		tempMeta.References = []models.ArtifactReference{tempRef}
		_, err = h.storage.UpdateMeta(ctx, *tempMeta)
		if err != nil {
			return fmt.Errorf("failed to add cleanup reference: %w", err)
		}
	}

	// Find and delete the cleanup reference
	cleanupRef := models.ArtifactReference{
		Name: "temp-cleanup",
		Repo: "temp-cleanup",
	}
	_, err = h.storage.DeleteArtifact(ctx, tempHash, cleanupRef)
	return err
}

// handleExistingHash handles the case where the computed hash already exists.
// It cleans up the temp file and merges references if provided.
func (h *HashComputingArtifactStorage) handleExistingHash(
	ctx context.Context,
	computedHash string,
	tempHash string,
	tempMeta *models.ArtifactMeta,
	meta *models.ArtifactMeta,
) (*models.ArtifactMeta, error) {
	// Cleanup temp file
	if cleanupErr := h.cleanupTempHash(ctx, tempHash); cleanupErr != nil {
		// Log cleanup error but continue with merge
		// The temp file will be cleaned up later or remain in trash
	}

	// Get existing metadata
	existingMeta, err := h.storage.GetMeta(ctx, computedHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing metadata: %w", err)
	}

	// Merge references if provided (follow normal Create behavior for existing artifacts)
	if meta != nil && len(meta.References) > 0 {
		mergeSize := existingMeta.Length
		if tempMeta != nil {
			mergeSize = tempMeta.Length
		}
		return h.storage.CreateArtifact(ctx, computedHash, bytes.NewReader(nil), mergeSize, meta)
	}

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
		existingMeta, checkErr := h.storage.GetMeta(ctx, computedHash)
		if checkErr == nil && existingMeta != nil {
			// Hash exists now (race condition: another goroutine created it)
			// Handle it as existing hash
			return h.handleExistingHash(ctx, computedHash, tempHash, tempMeta, meta)
		}
		// Move failed for other reason, cleanup and return error
		h.cleanupTempHash(ctx, tempHash)
		return nil, fmt.Errorf("failed to move from temp to final hash: %w", err)
	}

	// Update metadata with computed hash
	if tempMeta != nil {
		tempMeta.Hash = computedHash
		updatedMeta, err := h.storage.UpdateMeta(ctx, *tempMeta)
		if err != nil {
			// Metadata update failed, but file is already moved
			// Try to get the metadata
			return h.storage.GetMeta(ctx, computedHash)
		}
		return updatedMeta, nil
	}

	// Get final metadata
	return h.storage.GetMeta(ctx, computedHash)
}

// CreateArtifact streams data from 'r' to storage.
// If hash is unknown (empty, len<3, or "UNKNOWN"), computes SHA-256 hash automatically.
func (h *HashComputingArtifactStorage) CreateArtifact(ctx context.Context, hash string, r io.Reader, size int64, meta *models.ArtifactMeta) (*models.ArtifactMeta, error) {
	// Check if hash is unknown
	if !h.isUnknownHash(hash) {
		// Known hash: delegate directly to underlying storage
		return h.storage.CreateArtifact(ctx, hash, r, size, meta)
	}

	// Unknown hash: compute it using temp storage approach
	tempHash := h.generateTempHash()

	// Create SHA-256 hasher
	hasher := sha256.New()

	// Use TeeReader to compute hash while streaming to storage
	teeReader := io.TeeReader(r, hasher)

	// Create artifact with temp hash (this streams the data)
	tempMeta, err := h.storage.CreateArtifact(ctx, tempHash, teeReader, size, meta)
	if err != nil {
		return nil, fmt.Errorf("failed to create with temp hash: %w", err)
	}

	// Compute final hash from hasher
	computedHash := hex.EncodeToString(hasher.Sum(nil))

	// Check if computed hash already exists
	existingMeta, err := h.storage.GetMeta(ctx, computedHash)
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
func (h *HashComputingArtifactStorage) DeleteArtifact(ctx context.Context, hash string, ref models.ArtifactReference) (*models.ArtifactMeta, error) {
	return h.storage.DeleteArtifact(ctx, hash, ref)
}

// GetMeta reads the metadata JSON file.
func (h *HashComputingArtifactStorage) GetMeta(ctx context.Context, hash string) (*models.ArtifactMeta, error) {
	return h.storage.GetMeta(ctx, hash)
}

// UpdateMeta overwrites the metadata JSON file.
func (h *HashComputingArtifactStorage) UpdateMeta(ctx context.Context, meta models.ArtifactMeta) (*models.ArtifactMeta, error) {
	return h.storage.UpdateMeta(ctx, meta)
}
