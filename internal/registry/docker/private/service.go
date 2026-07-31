package private

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/collective-projects/brm-server/internal/registry/docker"
	"github.com/collective-projects/brm-server/internal/storage"
	"github.com/collective-projects/brm-server/pkg/models"

	"github.com/google/uuid"
)

// DockerRegistryPrivateService handles core registry logic for private registries
type DockerRegistryPrivateService struct {
	storage       models.ArtifactStorage
	registryAlias string
	description   string
	logger        *slog.Logger

	// Blob upload session management
	uploadSessions map[string]*UploadSession
	sessionsMutex  sync.RWMutex
}

// UploadSession tracks an active chunked blob upload. Chunk data is streamed to a temp file
// on disk as it arrives (rather than accumulated in memory) so an upload's memory footprint
// stays flat regardless of blob size.
type UploadSession struct {
	UUID      string
	Name      string
	Offset    int64
	CreatedAt time.Time

	mu   sync.Mutex
	file *os.File
}

// NewDockerRegistryPrivateService creates a new private Docker registry service
func NewDockerRegistryPrivateService(
	registryAlias string,
	storageAlias string,
	description string,
) (*DockerRegistryPrivateService, error) {
	service := &DockerRegistryPrivateService{
		registryAlias:  registryAlias,
		description:    description,
		uploadSessions: make(map[string]*UploadSession),
		logger:         slog.Default().With("component", "docker-registry-private", "registry", registryAlias),
	}

	service.logger.Debug("Private Docker registry service created", "storageAlias", storageAlias)

	// Start cleanup goroutine for expired sessions
	go service.cleanupExpiredSessions()

	return service, nil
}

// SetStorage sets the storage backend (called after storage is resolved)
func (s *DockerRegistryPrivateService) SetStorage(storage models.ArtifactStorage) {
	s.storage = storage
}

// cleanupExpiredSessions periodically removes expired upload sessions and their temp files.
func (s *DockerRegistryPrivateService) cleanupExpiredSessions() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.sessionsMutex.Lock()
		now := time.Now()
		for uuid, session := range s.uploadSessions {
			if now.Sub(session.CreatedAt) > 1*time.Hour {
				delete(s.uploadSessions, uuid)
				session.discard(s.logger)
			}
		}
		s.sessionsMutex.Unlock()
	}
}

// discard closes and removes the session's temp file. Safe to call multiple times.
func (session *UploadSession) discard(logger *slog.Logger) {
	session.mu.Lock()
	defer session.mu.Unlock()

	if session.file == nil {
		return
	}
	name := session.file.Name()
	if err := session.file.Close(); err != nil {
		logger.Warn("failed to close upload session temp file", "uuid", session.UUID, "path", name, "error", err)
	}
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		logger.Warn("failed to remove upload session temp file", "uuid", session.UUID, "path", name, "error", err)
	}
	session.file = nil
}

// getStorageKey generates a storage key for a manifest or blob (using digest for content-addressable storage)
// Strips the algorithm prefix (e.g., "sha256:") to use just the hex hash for storage.
// Docker digests are in format "algorithm:hexhash" (e.g., "sha256:abc123..."); for storage,
// we only need the hex part. Write paths (PutBlob) additionally require the algorithm to be
// sha256 before trusting client-supplied digests as a storage key.
func (s *DockerRegistryPrivateService) getStorageKey(digest string) string {
	if idx := strings.Index(digest, ":"); idx >= 0 {
		return digest[idx+1:]
	}
	return digest
}

// getManifestRefKey generates a key for manifest reference mapping
// Calculates SHA256 hash of the reference string for content-addressable storage
func (s *DockerRegistryPrivateService) getManifestRefKey(name, reference string) string {
	// Create a deterministic key from name and reference
	key := fmt.Sprintf("%s::%s", name, reference)
	// Hash the key to get a proper storage hash
	hasher := sha256.New()
	hasher.Write([]byte(key))
	return hex.EncodeToString(hasher.Sum(nil))
}

// calculateDigest calculates SHA256 digest
func (s *DockerRegistryPrivateService) calculateDigest(data []byte) string {
	hasher := sha256.New()
	hasher.Write(data)
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

// CalculateDigest calculates SHA256 digest (exported for use in handlers)
func (s *DockerRegistryPrivateService) CalculateDigest(data []byte) string {
	return s.calculateDigest(data)
}

// GetManifest retrieves a manifest by name and reference
func (s *DockerRegistryPrivateService) GetManifest(ctx context.Context, name, reference string) ([]byte, string, error) {
	s.logger.Debug("GetManifest called", "name", name, "reference", reference)

	// Look up the manifest by reference (uses ref/ folder internally)
	// Match the naming used in PutManifest: "{repo}:{tag}"
	manifestRef := models.ArtifactReference{
		Name:     fmt.Sprintf("%s:%s", name, reference),
		Registry: s.registryAlias,
	}

	// Get metadata to resolve reference to hash
	meta, err := s.storage.GetMeta(ctx, models.ArtifactIdentifier{Reference: &manifestRef})
	if err != nil {
		s.logger.Debug("Manifest reference not found", "ref", manifestRef.Name, "error", err)
		return nil, "", fmt.Errorf("manifest reference not found: %w", err)
	}

	// Retrieve manifest by digest (hash is already without "sha256:" prefix)
	storageKey := meta.Hash
	readReq := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: storageKey},
		Range: models.ByteRange{
			Offset: 0,
			Length: -1,
		},
	}

	rc, _, err := s.storage.ReadBlob(ctx, readReq)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read manifest: %w", err)
	}
	defer rc.Close()

	manifestData, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read manifest data: %w", err)
	}

	// Parse manifest to determine media type
	manifest, err := docker.ParseManifest(manifestData)
	if err != nil {
		// If parsing fails, try to determine media type from content
		// Default to OCI manifest
		return manifestData, docker.MediaTypeOCIManifest, nil
	}

	// Determine media type from parsed manifest
	mediaType := docker.MediaTypeOCIManifest
	if manifest.MediaType != "" {
		mediaType = manifest.MediaType
	}

	return manifestData, mediaType, nil
}

// CheckManifestExists checks if a manifest exists
func (s *DockerRegistryPrivateService) CheckManifestExists(ctx context.Context, name, reference string) (bool, string, error) {
	s.logger.Debug("CheckManifestExists called", "name", name, "reference", reference)

	manifestRef := models.ArtifactReference{
		Name:     fmt.Sprintf("%s:%s", name, reference),
		Registry: s.registryAlias,
	}

	meta, err := s.storage.GetMeta(ctx, models.ArtifactIdentifier{Reference: &manifestRef})
	if err != nil {
		s.logger.Debug("Manifest not found", "ref", manifestRef.Name, "error", err)
		return false, "", nil // Not found, not an error
	}

	// Return digest with sha256: prefix
	digest := "sha256:" + meta.Hash
	return true, digest, nil
}

// GetBlob retrieves a blob by digest
func (s *DockerRegistryPrivateService) GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, int64, error) {
	s.logger.Debug("GetBlob called", "name", name, "digest", digest)

	storageKey := s.getStorageKey(digest)

	// Check if blob exists
	meta, err := s.storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: storageKey})
	if err != nil {
		s.logger.Debug("Blob not found", "storageKey", storageKey, "error", err)
		return nil, 0, fmt.Errorf("blob not found: %w", err)
	}

	// Read blob
	readReq := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: storageKey},
		Range: models.ByteRange{
			Offset: 0,
			Length: -1,
		},
	}

	rc, _, err := s.storage.ReadBlob(ctx, readReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read blob: %w", err)
	}

	return rc, meta.Length, nil
}

// CheckBlobExists checks if a blob exists
func (s *DockerRegistryPrivateService) CheckBlobExists(ctx context.Context, name, digest string) (bool, int64, error) {
	s.logger.Debug("CheckBlobExists called", "name", name, "digest", digest)

	storageKey := s.getStorageKey(digest)
	meta, err := s.storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: storageKey})
	if err != nil {
		s.logger.Debug("Blob not found", "storageKey", storageKey, "error", err)
		return false, 0, nil // Not found, not an error
	}

	s.logger.Debug("Blob exists", "storageKey", storageKey, "size", meta.Length)
	return true, meta.Length, nil
}

// PutManifest stores a manifest and creates a reference mapping
func (s *DockerRegistryPrivateService) PutManifest(ctx context.Context, name, reference string, data []byte, mediaType string) error {
	s.logger.Debug("PutManifest called", "name", name, "reference", reference, "mediaType", mediaType, "size", len(data))

	// Calculate digest
	digest := s.calculateDigest(data)
	s.logger.Debug("Calculated manifest digest", "digest", digest)
	storageKey := s.getStorageKey(digest)

	// Create TWO references for the manifest:
	// 1. Tag-specific reference: {repo}:{tag} - for pulling by tag
	// 2. Generic repo reference: {repo} - for tracking which repos use this manifest
	tagRef := models.ArtifactReference{
		Name:                fmt.Sprintf("%s:%s", name, reference),
		Registry:            s.registryAlias,
		ReferencedTimestamp: time.Now().Unix(),
	}
	repoRef := models.ArtifactReference{
		Name:                name,
		Registry:            s.registryAlias,
		ReferencedTimestamp: time.Now().Unix(),
	}

	meta := &models.ArtifactMeta{
		Hash:             storageKey,
		Length:           int64(len(data)),
		CreatedTimestamp: time.Now().Unix(),
	}

	// Store manifest data by hash. The digest here is self-computed from data (not
	// client-claimed), so it's already trustworthy — no staging/validation needed.
	_, err := s.storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: storageKey}, bytes.NewReader(data), int64(len(data)), meta)
	if err != nil {
		if _, ok := err.(*models.HashConflictError); !ok {
			return fmt.Errorf("failed to store manifest: %w", err)
		}
		// Hash conflict is OK, artifact already exists
	}

	// Create both reference links by calling CreateArtifact with each reference
	// This adds the references to the metaref/ folder
	// Use -1 for size since artifact already exists (skips length validation)
	for _, ref := range []models.ArtifactReference{tagRef, repoRef} {
		refMeta := &models.ArtifactMeta{
			Hash:             storageKey,
			Length:           int64(len(data)),
			CreatedTimestamp: time.Now().Unix(),
		}
		refID := models.ArtifactIdentifier{
			Hash:      storageKey,
			Reference: &ref,
		}
		_, err := s.storage.CreateArtifact(ctx, refID, nil, -1, refMeta)
		if err != nil {
			return fmt.Errorf("failed to create reference %s::%s: %w", ref.Registry, ref.Name, err)
		}
	}

	return nil
}

// StartBlobUpload creates a new blob upload session backed by a temp file on disk.
func (s *DockerRegistryPrivateService) StartBlobUpload(ctx context.Context, name string) (string, error) {
	s.logger.Debug("StartBlobUpload called", "name", name)

	sessionUUID := uuid.New().String()

	file, err := os.CreateTemp("", "brm-blob-upload-*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create upload session temp file: %w", err)
	}

	session := &UploadSession{
		UUID:      sessionUUID,
		Name:      name,
		Offset:    0,
		CreatedAt: time.Now(),
		file:      file,
	}

	s.sessionsMutex.Lock()
	s.uploadSessions[sessionUUID] = session
	s.sessionsMutex.Unlock()

	s.logger.Debug("Blob upload session created", "uuid", sessionUUID, "name", name)
	return sessionUUID, nil
}

// UploadBlobChunk streams a chunk of blob data directly to the session's temp file (never
// buffering the whole blob in memory), and returns the new total offset.
func (s *DockerRegistryPrivateService) UploadBlobChunk(ctx context.Context, name, sessionUUID string, data io.Reader, offset int64) (int64, error) {
	s.sessionsMutex.RLock()
	session, exists := s.uploadSessions[sessionUUID]
	s.sessionsMutex.RUnlock()

	if !exists {
		return 0, fmt.Errorf("upload session not found")
	}

	if session.Name != name {
		return 0, fmt.Errorf("session name mismatch")
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.file == nil {
		return 0, fmt.Errorf("upload session already completed or discarded")
	}

	// Chunks are expected to arrive in order (offset matching the current end of the file);
	// out-of-order chunks are appended as-is, matching prior behavior.
	written, err := io.Copy(session.file, data)
	if err != nil {
		return 0, fmt.Errorf("failed to write chunk data: %w", err)
	}
	session.Offset += written

	return session.Offset, nil
}

// CompleteBlobUpload finalizes a blob upload, validates digest, and stores the blob.
// The final chunk data (if any) should be provided in the request body (for PUT with digest).
func (s *DockerRegistryPrivateService) CompleteBlobUpload(ctx context.Context, name, sessionUUID, digest string, finalChunk io.Reader) error {
	s.sessionsMutex.Lock()
	session, exists := s.uploadSessions[sessionUUID]
	if exists {
		delete(s.uploadSessions, sessionUUID)
	}
	s.sessionsMutex.Unlock()

	if !exists {
		return fmt.Errorf("upload session not found")
	}

	if session.Name != name {
		session.discard(s.logger)
		return fmt.Errorf("session name mismatch")
	}
	defer session.discard(s.logger)

	session.mu.Lock()
	if session.file == nil {
		session.mu.Unlock()
		return fmt.Errorf("upload session already completed or discarded")
	}
	if _, err := session.file.Seek(0, io.SeekStart); err != nil {
		session.mu.Unlock()
		return fmt.Errorf("failed to rewind upload session data: %w", err)
	}
	sessionFile := session.file
	session.mu.Unlock()

	// Stream the accumulated chunks plus any final chunk straight into PutBlob, without
	// buffering either in memory. Total size is left unknown (-1); PutBlob/storage handle that.
	var blobReader io.Reader = sessionFile
	if finalChunk != nil {
		blobReader = io.MultiReader(sessionFile, finalChunk)
	}

	return s.PutBlob(ctx, name, digest, blobReader, -1)
}

// PutBlob uploads a blob with digest validation.
//
// The stream is written to a temporary, unpublished storage key while being hashed; only if
// the computed SHA-256 digest matches the caller-supplied digest is the content moved into its
// final content-addressed location and a reference created. This ensures unvalidated content
// is never stored (or left stored) under a hash it doesn't actually match, and a client cannot
// force a digest under an unsupported/spoofed algorithm to be trusted as a storage key.
func (s *DockerRegistryPrivateService) PutBlob(ctx context.Context, name, digest string, reader io.Reader, size int64) error {
	s.logger.Debug("PutBlob called", "name", name, "digest", digest, "size", size)

	if !strings.HasPrefix(digest, "sha256:") {
		return fmt.Errorf("unsupported digest algorithm (only sha256 is supported): %s", digest)
	}
	storageKey := s.getStorageKey(digest)

	tempKey := "upload-" + uuid.New().String()
	hasher := sha256.New()
	teeReader := io.TeeReader(reader, hasher)

	if _, err := s.storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: tempKey}, teeReader, size, nil); err != nil {
		return fmt.Errorf("failed to stage blob upload: %w", err)
	}

	calculatedDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if calculatedDigest != digest {
		if cleanupErr := s.storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: tempKey}); cleanupErr != nil {
			s.logger.Warn("failed to clean up staged blob after digest mismatch", "tempKey", tempKey, "error", cleanupErr)
		}
		return fmt.Errorf("digest mismatch: expected %s, got %s", digest, calculatedDigest)
	}

	if err := s.publishStagedBlob(ctx, tempKey, storageKey); err != nil {
		return fmt.Errorf("failed to publish blob %s: %w", storageKey, err)
	}

	// Create reference link now that the blob is verified and published under storageKey.
	ref := models.ArtifactReference{
		Name:                name,
		Registry:            s.registryAlias,
		ReferencedTimestamp: time.Now().Unix(),
	}
	refMeta := &models.ArtifactMeta{
		Hash:             storageKey,
		CreatedTimestamp: time.Now().Unix(),
	}
	refID := models.ArtifactIdentifier{
		Hash:      storageKey,
		Reference: &ref,
	}
	if _, err := s.storage.CreateArtifact(ctx, refID, nil, -1, refMeta); err != nil {
		return fmt.Errorf("failed to create reference %s::%s: %w", ref.Registry, ref.Name, err)
	}

	return nil
}

// publishStagedBlob moves a validated, temporarily-staged blob to its final content-addressed
// location. If another upload already published the same content concurrently (Move fails
// because storageKey now exists), the staged copy is discarded instead of erroring.
func (s *DockerRegistryPrivateService) publishStagedBlob(ctx context.Context, tempKey, storageKey string) error {
	moveStorage, ok := s.storage.(storage.MoveStorage)
	if !ok {
		return fmt.Errorf("storage does not support Move, required to publish validated blobs")
	}

	if err := moveStorage.Move(ctx, tempKey, storageKey); err != nil {
		if _, getErr := s.storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: storageKey}); getErr == nil {
			// Another concurrent upload published the same content first; discard ours.
			if cleanupErr := s.storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: tempKey}); cleanupErr != nil {
				s.logger.Warn("failed to clean up staged blob after concurrent publish", "tempKey", tempKey, "error", cleanupErr)
			}
			return nil
		}
		if cleanupErr := s.storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: tempKey}); cleanupErr != nil {
			s.logger.Warn("failed to clean up staged blob after failed publish", "tempKey", tempKey, "error", cleanupErr)
		}
		return err
	}
	return nil
}
