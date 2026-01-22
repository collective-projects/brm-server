package private

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"testing"

	"github.com/collective-projects/brm-server/internal/storage"
	"github.com/collective-projects/brm-server/pkg/models"
)

// setupTestStorage creates a test storage instance
func setupTestStorage(t *testing.T) models.ArtifactStorage {
	baseDir := t.TempDir()
	storage, err := storage.NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create test storage: %v", err)
	}
	return storage
}

// setupTestService creates a test service with storage
func setupTestService(t *testing.T) (*DockerRegistryPrivateService, models.ArtifactStorage) {
	testStorage := setupTestStorage(t)
	service, err := NewDockerRegistryPrivateService("test-storage", "test description")
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}
	service.SetStorage(testStorage)
	return service, testStorage
}

// TestDockerRegistryPrivateServicePutManifest tests manifest storage
func TestDockerRegistryPrivateServicePutManifest(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	manifestData := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`)
	name := "test-repo"
	reference := "latest"
	mediaType := "application/vnd.docker.distribution.manifest.v2+json"

	err := service.PutManifest(ctx, name, reference, manifestData, mediaType)
	if err != nil {
		t.Fatalf("PutManifest failed: %v", err)
	}

	// Verify reference mapping exists and contains digest
	digest := service.CalculateDigest(manifestData)
	// Calculate the same hash that the service generates for reference mapping
	key := fmt.Sprintf("%s::%s", name, reference)
	hasher := sha256.New()
	hasher.Write([]byte(key))
	refKey := hex.EncodeToString(hasher.Sum(nil))
	refMeta, err := service.storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: refKey})
	if err != nil {
		t.Fatalf("Reference mapping not found: %v", err)
	}
	// Extract digest from References
	foundDigest := ""
	for _, ref := range refMeta.References {
		if ref.Repo == "digest" {
			foundDigest = ref.Name
			break
		}
	}
	if foundDigest != digest {
		t.Errorf("Reference mapping digest mismatch: expected %s, got %s", digest, foundDigest)
		// Continue test even if this fails to see if manifest can still be retrieved
	}

	// Verify manifest can be retrieved
	retrievedData, retrievedMediaType, err := service.GetManifest(ctx, name, reference)
	if err != nil {
		t.Fatalf("GetManifest failed: %v", err)
	}

	if len(retrievedData) == 0 {
		t.Error("Retrieved manifest data is empty")
	}

	if !bytes.Equal(retrievedData, manifestData) {
		t.Errorf("Manifest data mismatch: expected %s, got %s", string(manifestData), string(retrievedData))
	}

	if retrievedMediaType != mediaType {
		t.Errorf("Media type mismatch: expected %s, got %s", mediaType, retrievedMediaType)
	}
}

// TestDockerRegistryPrivateServiceGetManifestNotFound tests getting non-existent manifest
func TestDockerRegistryPrivateServiceGetManifestNotFound(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	_, _, err := service.GetManifest(ctx, "nonexistent", "latest")
	if err == nil {
		t.Error("Expected error for non-existent manifest, got nil")
	}
}

// TestDockerRegistryPrivateServiceCheckManifestExists tests manifest existence check
func TestDockerRegistryPrivateServiceCheckManifestExists(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	manifestData := []byte(`{"schemaVersion":2}`)
	name := "test-repo"
	reference := "v1.0.0"

	// Manifest doesn't exist yet
	exists, _, err := service.CheckManifestExists(ctx, name, reference)
	if err != nil {
		t.Fatalf("CheckManifestExists failed: %v", err)
	}
	if exists {
		t.Error("Manifest should not exist yet")
	}

	// Store manifest
	err = service.PutManifest(ctx, name, reference, manifestData, "application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		t.Fatalf("PutManifest failed: %v", err)
	}

	// Now it should exist
	exists, digest, err := service.CheckManifestExists(ctx, name, reference)
	if err != nil {
		t.Fatalf("CheckManifestExists failed: %v", err)
	}
	if !exists {
		t.Error("Manifest should exist now")
	}
	if digest == "" {
		t.Error("Digest should not be empty")
	}
}

// TestDockerRegistryPrivateServicePutBlob tests blob storage
func TestDockerRegistryPrivateServicePutBlob(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	blobData := []byte("test blob data")
	name := "test-repo"
	digest := service.CalculateDigest(blobData)

	err := service.PutBlob(ctx, name, digest, bytes.NewReader(blobData), int64(len(blobData)))
	if err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}

	// Verify blob can be retrieved
	reader, size, err := service.GetBlob(ctx, name, digest)
	if err != nil {
		t.Fatalf("GetBlob failed: %v", err)
	}
	defer reader.Close()

	if size != int64(len(blobData)) {
		t.Errorf("Size mismatch: expected %d, got %d", len(blobData), size)
	}

	retrievedData, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to read blob: %v", err)
	}

	if !bytes.Equal(retrievedData, blobData) {
		t.Errorf("Blob data mismatch: expected %s, got %s", string(blobData), string(retrievedData))
	}
}

// TestDockerRegistryPrivateServicePutBlobDigestMismatch tests blob upload with wrong digest
func TestDockerRegistryPrivateServicePutBlobDigestMismatch(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	blobData := []byte("test blob data")
	name := "test-repo"
	wrongDigest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	err := service.PutBlob(ctx, name, wrongDigest, bytes.NewReader(blobData), int64(len(blobData)))
	if err == nil {
		t.Error("Expected error for digest mismatch, got nil")
	}
}

// TestDockerRegistryPrivateServiceCheckBlobExists tests blob existence check
func TestDockerRegistryPrivateServiceCheckBlobExists(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	blobData := []byte("test blob")
	name := "test-repo"
	digest := service.CalculateDigest(blobData)

	// Blob doesn't exist yet
	exists, _, err := service.CheckBlobExists(ctx, name, digest)
	if err != nil {
		t.Fatalf("CheckBlobExists failed: %v", err)
	}
	if exists {
		t.Error("Blob should not exist yet")
	}

	// Store blob
	err = service.PutBlob(ctx, name, digest, bytes.NewReader(blobData), int64(len(blobData)))
	if err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}

	// Now it should exist
	exists, size, err := service.CheckBlobExists(ctx, name, digest)
	if err != nil {
		t.Fatalf("CheckBlobExists failed: %v", err)
	}
	if !exists {
		t.Error("Blob should exist now")
	}
	if size != int64(len(blobData)) {
		t.Errorf("Size mismatch: expected %d, got %d", len(blobData), size)
	}
}

// TestDockerRegistryPrivateServiceBlobUploadSession tests blob upload session flow
func TestDockerRegistryPrivateServiceBlobUploadSession(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	name := "test-repo"

	// Start upload session
	uuid, err := service.StartBlobUpload(ctx, name)
	if err != nil {
		t.Fatalf("StartBlobUpload failed: %v", err)
	}
	if uuid == "" {
		t.Error("UUID should not be empty")
	}

	// Upload chunk
	chunk1 := []byte("chunk1")
	offset, err := service.UploadBlobChunk(ctx, name, uuid, bytes.NewReader(chunk1), 0)
	if err != nil {
		t.Fatalf("UploadBlobChunk failed: %v", err)
	}
	if offset != int64(len(chunk1)) {
		t.Errorf("Offset mismatch: expected %d, got %d", len(chunk1), offset)
	}

	// Upload another chunk
	chunk2 := []byte("chunk2")
	offset, err = service.UploadBlobChunk(ctx, name, uuid, bytes.NewReader(chunk2), offset)
	if err != nil {
		t.Fatalf("UploadBlobChunk failed: %v", err)
	}
	if offset != int64(len(chunk1)+len(chunk2)) {
		t.Errorf("Offset mismatch: expected %d, got %d", len(chunk1)+len(chunk2), offset)
	}

	// Complete upload
	finalChunk := []byte("final")
	combinedData := append(append(chunk1, chunk2...), finalChunk...)
	digest := service.CalculateDigest(combinedData)

	err = service.CompleteBlobUpload(ctx, name, uuid, digest, bytes.NewReader(finalChunk))
	if err != nil {
		t.Fatalf("CompleteBlobUpload failed: %v", err)
	}

	// Verify blob can be retrieved
	reader, size, err := service.GetBlob(ctx, name, digest)
	if err != nil {
		t.Fatalf("GetBlob failed: %v", err)
	}
	defer reader.Close()

	if size != int64(len(combinedData)) {
		t.Errorf("Size mismatch: expected %d, got %d", len(combinedData), size)
	}

	retrievedData, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to read blob: %v", err)
	}

	if !bytes.Equal(retrievedData, combinedData) {
		t.Errorf("Blob data mismatch")
	}
}

// TestDockerRegistryPrivateServiceBlobUploadSessionNotFound tests completing non-existent session
func TestDockerRegistryPrivateServiceBlobUploadSessionNotFound(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	err := service.CompleteBlobUpload(ctx, "test-repo", "nonexistent-uuid", "sha256:abc", nil)
	if err == nil {
		t.Error("Expected error for non-existent session, got nil")
	}
}

// TestDockerRegistryPrivateServiceBlobUploadSingleRequest tests single-request blob upload
func TestDockerRegistryPrivateServiceBlobUploadSingleRequest(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	blobData := []byte("single request blob")
	name := "test-repo"
	digest := service.CalculateDigest(blobData)

	err := service.PutBlob(ctx, name, digest, bytes.NewReader(blobData), int64(len(blobData)))
	if err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}

	// Verify blob exists
	exists, size, err := service.CheckBlobExists(ctx, name, digest)
	if err != nil {
		t.Fatalf("CheckBlobExists failed: %v", err)
	}
	if !exists {
		t.Error("Blob should exist")
	}
	if size != int64(len(blobData)) {
		t.Errorf("Size mismatch: expected %d, got %d", len(blobData), size)
	}
}

// TestDockerRegistryPrivateServiceMultipleManifestReferences tests multiple references to same manifest
func TestDockerRegistryPrivateServiceMultipleManifestReferences(t *testing.T) {
	service, _ := setupTestService(t)
	ctx := context.Background()

	manifestData := []byte(`{"schemaVersion":2}`)
	name := "test-repo"
	mediaType := "application/vnd.docker.distribution.manifest.v2+json"

	// Store with first reference
	err := service.PutManifest(ctx, name, "latest", manifestData, mediaType)
	if err != nil {
		t.Fatalf("PutManifest failed: %v", err)
	}

	// Store with second reference (same manifest, different tag)
	err = service.PutManifest(ctx, name, "v1.0.0", manifestData, mediaType)
	if err != nil {
		t.Fatalf("PutManifest failed: %v", err)
	}

	// Both references should work
	_, _, err = service.GetManifest(ctx, name, "latest")
	if err != nil {
		t.Errorf("GetManifest with 'latest' failed: %v", err)
	}

	_, _, err = service.GetManifest(ctx, name, "v1.0.0")
	if err != nil {
		t.Errorf("GetManifest with 'v1.0.0' failed: %v", err)
	}
}

// TestDockerRegistryPrivateServiceCalculateDigest tests digest calculation
func TestDockerRegistryPrivateServiceCalculateDigest(t *testing.T) {
	service, _ := setupTestService(t)

	data := []byte("test data")
	digest1 := service.CalculateDigest(data)
	digest2 := service.CalculateDigest(data)

	if digest1 != digest2 {
		t.Error("Digest should be deterministic")
	}

	if !bytes.HasPrefix([]byte(digest1), []byte("sha256:")) {
		t.Errorf("Digest should start with 'sha256:', got %s", digest1)
	}
}
