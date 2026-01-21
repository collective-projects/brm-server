package private

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/collective-projects/brm-server/internal/registry/docker"
	"github.com/collective-projects/brm-server/pkg/models"
)

// DockerRegistryPrivateService handles core registry logic for private registries
type DockerRegistryPrivateService struct {
	storage     models.ArtifactStorage
	description string
	logger      *slog.Logger

	// Blob upload session management
	uploadSessions map[string]*UploadSession
	sessionsMutex  sync.RWMutex
}

// UploadSession tracks an active blob upload
type UploadSession struct {
	UUID      string
	Name      string
	Size      int64
	Offset    int64
	CreatedAt time.Time
	Data      *bytes.Buffer // Accumulated blob data (for chunked uploads)
}

// NewDockerRegistryPrivateService creates a new private Docker registry service
func NewDockerRegistryPrivateService(
	storageAlias string,
	description string,
) (*DockerRegistryPrivateService, error) {
	service := &DockerRegistryPrivateService{
		description:    description,
		uploadSessions: make(map[string]*UploadSession),
		logger:         slog.Default().With("component", "docker-registry-private", "description", description),
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

// cleanupExpiredSessions periodically removes expired upload sessions
func (s *DockerRegistryPrivateService) cleanupExpiredSessions() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.sessionsMutex.Lock()
		now := time.Now()
		for uuid, session := range s.uploadSessions {
			if now.Sub(session.CreatedAt) > 1*time.Hour {
				delete(s.uploadSessions, uuid)
			}
		}
		s.sessionsMutex.Unlock()
	}
}

// getStorageKey generates a storage key for a manifest or blob (using digest for content-addressable storage)
// Strips the algorithm prefix (e.g., "sha256:") to use just the hex hash for storage
func (s *DockerRegistryPrivateService) getStorageKey(digest string) string {
	// TODO: We are thrusting the client to provide the correct digest format.
	//       This may be recorded, validated, or normalized in the future.
	//       Also, can this be used as a vulnerability if the client provides unexpected/wrong digests/hashes?
	// Docker digests are in format "algorithm:hexhash" (e.g., "sha256:abc123...")
	// For storage, we only need the hex part
	if idx := strings.Index(digest, ":"); idx >= 0 {
		return digest[idx+1:]
	}
	return digest
}

// getManifestRefKey generates a key for manifest reference mapping
// Uses a safe format without colons to avoid filesystem issues
func (s *DockerRegistryPrivateService) getManifestRefKey(name, reference string) string {
	//TODO: All blob/filenames will be hashes, which represent the file data, not the original names.
	// 		Change this when we find a way to quickly access hashes from requested filenames.

	// Replace colons and slashes with safe characters to avoid filesystem path issues
	safeName := strings.ReplaceAll(strings.ReplaceAll(name, ":", "_"), "/", "_")
	safeReference := strings.ReplaceAll(reference, ":", "_")
	return fmt.Sprintf("manifest-ref_%s_%s", safeName, safeReference)
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

// validateDigest validates that the content matches the expected digest
func (s *DockerRegistryPrivateService) validateDigest(reader io.Reader, expectedDigest string, size int64) error {
	hasher := sha256.New()
	written, err := io.Copy(hasher, reader)
	if err != nil {
		return fmt.Errorf("failed to calculate digest: %w", err)
	}

	if size >= 0 && written != size {
		return fmt.Errorf("size mismatch: expected %d bytes, got %d", size, written)
	}

	calculatedDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if calculatedDigest != expectedDigest {
		return fmt.Errorf("digest mismatch: expected %s, got %s", expectedDigest, calculatedDigest)
	}

	return nil
}

// GetManifest retrieves a manifest by name and reference
func (s *DockerRegistryPrivateService) GetManifest(ctx context.Context, name, reference string) ([]byte, string, error) {
	s.logger.Debug("GetManifest called", "name", name, "reference", reference)

	// First, look up the digest from the reference mapping
	refKey := s.getManifestRefKey(name, reference)
	s.logger.Debug("Looking up manifest reference", "refKey", refKey)
	meta, err := s.storage.GetMeta(ctx, refKey)
	if err != nil {
		s.logger.Debug("Manifest reference not found", "refKey", refKey, "error", err)
		return nil, "", fmt.Errorf("manifest reference not found: %w", err)
	}

	// Extract digest from metadata (stored in References with Repo="digest")
	digest := ""
	for _, ref := range meta.References {
		if ref.Repo == "digest" {
			digest = ref.Name
			break
		}
	}
	if digest == "" {
		return nil, "", fmt.Errorf("invalid manifest reference: digest not found")
	}

	// Retrieve manifest by digest
	storageKey := s.getStorageKey(digest)
	readReq := models.ArtifactRange{
		Hash: storageKey,
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

	refKey := s.getManifestRefKey(name, reference)
	meta, err := s.storage.GetMeta(ctx, refKey)
	if err != nil {
		s.logger.Debug("Manifest not found", "refKey", refKey, "error", err)
		return false, "", nil // Not found, not an error
	}

	// Extract digest from References (stored with Repo="digest")
	digest := ""
	for _, ref := range meta.References {
		if ref.Repo == "digest" {
			digest = ref.Name
			break
		}
	}
	if digest == "" {
		return false, "", nil
	}

	// Verify the manifest actually exists
	storageKey := s.getStorageKey(digest)
	_, err = s.storage.GetMeta(ctx, storageKey)
	if err != nil {
		return false, "", nil
	}

	return true, digest, nil
}

// GetBlob retrieves a blob by digest
func (s *DockerRegistryPrivateService) GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, int64, error) {
	s.logger.Debug("GetBlob called", "name", name, "digest", digest)

	storageKey := s.getStorageKey(digest)

	// Check if blob exists
	meta, err := s.storage.GetMeta(ctx, storageKey)
	if err != nil {
		s.logger.Debug("Blob not found", "storageKey", storageKey, "error", err)
		return nil, 0, fmt.Errorf("blob not found: %w", err)
	}

	// Read blob
	readReq := models.ArtifactRange{
		Hash: storageKey,
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
	meta, err := s.storage.GetMeta(ctx, storageKey)
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

	// Store manifest (content-addressable by digest)
	ref := models.ArtifactReference{
		Name:                name,
		Repo:                "manifest",
		ReferencedTimestamp: time.Now().Unix(),
	}
	meta := &models.ArtifactMeta{
		Hash:             storageKey,
		Length:           int64(len(data)),
		CreatedTimestamp: time.Now().Unix(),
		References:       []models.ArtifactReference{ref},
	}

	// Store manifest data
	_, err := s.storage.CreateArtifact(ctx, storageKey, bytes.NewReader(data), int64(len(data)), meta)
	if err != nil {
		// If artifact exists (HashConflictError), merge references
		if _, ok := err.(*models.HashConflictError); ok {
			existingMeta, getErr := s.storage.GetMeta(ctx, storageKey)
			if getErr == nil {
				// Merge references
				existingMeta.References = append(existingMeta.References, ref)
				_, updateErr := s.storage.UpdateMeta(ctx, *existingMeta)
				if updateErr != nil {
					return fmt.Errorf("failed to update manifest metadata: %w", updateErr)
				}
			}
		} else {
			return fmt.Errorf("failed to store manifest: %w", err)
		}
	}

	// Create reference mapping: name/reference -> digest
	// Store the digest in the References field as a special reference
	refKey := s.getManifestRefKey(name, reference)
	refMeta := &models.ArtifactMeta{
		Hash:             refKey,
		Length:           0,
		CreatedTimestamp: time.Now().Unix(),
		// Store digest in References as a special reference with Repo="digest"
		References: []models.ArtifactReference{
			{
				Name:                digest, // Store digest in Name field
				Repo:                "digest",
				ReferencedTimestamp: time.Now().Unix(),
			},
		},
	}

	// Use empty reader for reference mapping (no data, just metadata)
	_, err = s.storage.CreateArtifact(ctx, refKey, bytes.NewReader([]byte{}), 0, refMeta)
	if err != nil {
		// If reference already exists, update it
		existingRefMeta, getErr := s.storage.GetMeta(ctx, refKey)
		if getErr == nil {
			// Update digest in References
			existingRefMeta.References = []models.ArtifactReference{
				{
					Name:                digest,
					Repo:                "digest",
					ReferencedTimestamp: time.Now().Unix(),
				},
			}
			_, updateErr := s.storage.UpdateMeta(ctx, *existingRefMeta)
			if updateErr != nil {
				return fmt.Errorf("failed to update manifest reference: %w", updateErr)
			}
		} else {
			return fmt.Errorf("failed to create manifest reference: %w", err)
		}
	}

	return nil
}

// StartBlobUpload creates a new blob upload session
func (s *DockerRegistryPrivateService) StartBlobUpload(ctx context.Context, name string) (string, error) {
	s.logger.Debug("StartBlobUpload called", "name", name)

	// Generate UUID for session
	uuid := fmt.Sprintf("%d-%d", time.Now().UnixNano(), len(s.uploadSessions))

	session := &UploadSession{
		UUID:      uuid,
		Name:      name,
		Size:      0,
		Offset:    0,
		CreatedAt: time.Now(),
	}

	s.sessionsMutex.Lock()
	s.uploadSessions[uuid] = session
	s.sessionsMutex.Unlock()

	s.logger.Debug("Blob upload session created", "uuid", uuid, "name", name)
	return uuid, nil
}

// UploadBlobChunk uploads a chunk of blob data to an existing session
func (s *DockerRegistryPrivateService) UploadBlobChunk(ctx context.Context, name, uuid string, data io.Reader, offset int64) (int64, error) {
	s.sessionsMutex.Lock()
	session, exists := s.uploadSessions[uuid]
	s.sessionsMutex.Unlock()

	if !exists {
		return 0, fmt.Errorf("upload session not found")
	}

	if session.Name != name {
		return 0, fmt.Errorf("session name mismatch")
	}

	// Read chunk data
	chunkData, err := io.ReadAll(data)
	if err != nil {
		return 0, fmt.Errorf("failed to read chunk data: %w", err)
	}

	chunkSize := int64(len(chunkData))

	// Update session - append chunk data
	s.sessionsMutex.Lock()
	if session.Data == nil {
		session.Data = &bytes.Buffer{}
	}
	// Write chunk at specified offset (or append if offset matches current size)
	if offset == session.Offset {
		session.Data.Write(chunkData)
		session.Offset += chunkSize
		session.Size += chunkSize
	} else {
		// Offset mismatch - for now, just append (could implement proper offset handling)
		session.Data.Write(chunkData)
		session.Offset = int64(session.Data.Len())
		session.Size = session.Offset
	}
	s.sessionsMutex.Unlock()

	return session.Offset, nil
}

// CompleteBlobUpload finalizes a blob upload, validates digest, and stores the blob
// The final chunk data should be provided in the request body (for PUT with digest)
func (s *DockerRegistryPrivateService) CompleteBlobUpload(ctx context.Context, name, uuid, digest string, finalChunk io.Reader) error {
	s.sessionsMutex.Lock()
	session, exists := s.uploadSessions[uuid]
	if exists {
		delete(s.uploadSessions, uuid)
	}
	s.sessionsMutex.Unlock()

	if !exists {
		return fmt.Errorf("upload session not found")
	}

	if session.Name != name {
		return fmt.Errorf("session name mismatch")
	}

	// Combine accumulated chunks with final chunk
	var blobReader io.Reader
	if session.Data != nil && session.Data.Len() > 0 {
		if finalChunk != nil {
			// Combine: accumulated data + final chunk
			blobReader = io.MultiReader(bytes.NewReader(session.Data.Bytes()), finalChunk)
		} else {
			// Only accumulated data
			blobReader = bytes.NewReader(session.Data.Bytes())
		}
	} else if finalChunk != nil {
		// Only final chunk
		blobReader = finalChunk
	} else {
		return fmt.Errorf("no blob data provided")
	}

	// Calculate total size
	totalSize := int64(-1)
	if session.Data != nil {
		totalSize = int64(session.Data.Len())
	}
	if finalChunk != nil {
		// Read final chunk to get size (we'll need to buffer it for validation anyway)
		finalData, err := io.ReadAll(finalChunk)
		if err != nil {
			return fmt.Errorf("failed to read final chunk: %w", err)
		}
		if totalSize >= 0 {
			totalSize += int64(len(finalData))
		} else {
			totalSize = int64(len(finalData))
		}
		blobReader = io.MultiReader(
			func() io.Reader {
				if session.Data != nil && session.Data.Len() > 0 {
					return bytes.NewReader(session.Data.Bytes())
				}
				return bytes.NewReader([]byte{})
			}(),
			bytes.NewReader(finalData),
		)
	}

	// Use PutBlob to validate and store
	return s.PutBlob(ctx, name, digest, blobReader, totalSize)
}

// PutBlob uploads a blob directly in a single request with digest validation
func (s *DockerRegistryPrivateService) PutBlob(ctx context.Context, name, digest string, reader io.Reader, size int64) error {
	s.logger.Debug("PutBlob called", "name", name, "digest", digest, "size", size)

	storageKey := s.getStorageKey(digest)

	// Use io.TeeReader to validate digest while streaming to storage
	hasher := sha256.New()
	teeReader := io.TeeReader(reader, hasher)

	// Store blob while calculating digest simultaneously
	ref := models.ArtifactReference{
		Name:                name,
		Repo:                "blob",
		ReferencedTimestamp: time.Now().Unix(),
	}
	meta := &models.ArtifactMeta{
		Hash:             storageKey,
		Length:           size,
		CreatedTimestamp: time.Now().Unix(),
		References:       []models.ArtifactReference{ref},
	}

	_, err := s.storage.CreateArtifact(ctx, storageKey, teeReader, size, meta)
	if err != nil {
		// If artifact exists (HashConflictError), merge references
		if _, ok := err.(*models.HashConflictError); ok {
			existingMeta, getErr := s.storage.GetMeta(ctx, storageKey)
			if getErr == nil {
				// Merge references
				existingMeta.References = append(existingMeta.References, ref)
				_, updateErr := s.storage.UpdateMeta(ctx, *existingMeta)
				if updateErr != nil {
					return fmt.Errorf("failed to update blob metadata: %w", updateErr)
				}
			}
			// Verify digest matches
			calculatedDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
			if calculatedDigest != digest {
				return fmt.Errorf("digest mismatch: expected %s, got %s", digest, calculatedDigest)
			}
			return nil
		}
		return fmt.Errorf("failed to store blob: %w", err)
	}

	// Validate digest after storage
	calculatedDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if calculatedDigest != digest {
		// Clean up: delete the artifact we just created
		// Note: This is a best-effort cleanup
		_, _ = s.storage.DeleteArtifact(ctx, storageKey, ref)
		return fmt.Errorf("digest mismatch: expected %s, got %s", digest, calculatedDigest)
	}

	return nil
}
