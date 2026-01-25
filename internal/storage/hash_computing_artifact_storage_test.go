package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/collective-projects/brm-server/pkg/models"
)

// TestHashComputingArtifactStorageUnknownHashDetection tests unknown hash detection
func TestHashComputingArtifactStorageUnknownHashDetection(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	testCases := []struct {
		name     string
		hash     string
		expected bool
	}{
		{"empty string", "", true},
		{"single character", "a", true},
		{"two characters", "ab", true},
		{"three characters", "abc", false},
		{"unknown uppercase", "UNKNOWN", true},
		{"unknown lowercase", "unknown", true},
		{"unknown mixed case", "UnKnOwN", true},
		{"normal hash", "abc123def456", false},
		{"sha256-like", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := wrapper.isUnknownHash(tc.hash)
			if result != tc.expected {
				t.Errorf("isUnknownHash(%q) = %v, expected %v", tc.hash, result, tc.expected)
			}
		})
	}
}

// TestHashComputingArtifactStorageKnownHashDelegation tests that known hashes delegate directly
func TestHashComputingArtifactStorageKnownHashDelegation(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	ctx := context.Background()
	hash := "knownhash123"
	testData := []byte("test data")

	// Create with known hash - should delegate directly
	meta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if meta.Hash != hash {
		t.Errorf("Expected hash %s, got %s", hash, meta.Hash)
	}

	// Verify data was stored correctly
	readReq := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: hash},
		Range: models.ByteRange{
			Offset: 0,
			Length: -1,
		},
	}
	rc, _, err := wrapper.ReadBlob(ctx, readReq)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Failed to read data: %v", err)
	}

	if !bytes.Equal(readData, testData) {
		t.Errorf("Data mismatch: expected %v, got %v", testData, readData)
	}
}

// TestHashComputingArtifactStorageEmptyHash tests Create with empty hash
func TestHashComputingArtifactStorageEmptyHash(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	ctx := context.Background()
	testData := []byte("test data for hash computation")

	// Create with empty hash
	meta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: ""}, bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if meta == nil {
		t.Fatal("Create returned nil metadata")
	}

	// Verify hash was computed correctly
	hasher := sha256.New()
	hasher.Write(testData)
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	if meta.Hash != expectedHash {
		t.Errorf("Expected hash %s, got %s", expectedHash, meta.Hash)
	}

	// Verify data can be read back using computed hash
	readReq := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: expectedHash},
		Range: models.ByteRange{
			Offset: 0,
			Length: -1,
		},
	}
	rc, _, err := wrapper.ReadBlob(ctx, readReq)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Failed to read data: %v", err)
	}

	if !bytes.Equal(readData, testData) {
		t.Errorf("Data mismatch: expected %v, got %v", testData, readData)
	}
}

// TestHashComputingArtifactStorageShortHash tests Create with hash length < 3
func TestHashComputingArtifactStorageShortHash(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	ctx := context.Background()
	testData := []byte("test data")

	// Create with short hash
	meta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: "ab"}, bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify hash was computed (should be SHA-256, not "ab")
	hasher := sha256.New()
	hasher.Write(testData)
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	if meta.Hash != expectedHash {
		t.Errorf("Expected hash %s, got %s", expectedHash, meta.Hash)
	}
}

// TestHashComputingArtifactStorageUnknownString tests Create with "UNKNOWN" hash
func TestHashComputingArtifactStorageUnknownString(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	ctx := context.Background()
	testData := []byte("test data")

	// Test different case variations
	for _, unknownHash := range []string{"UNKNOWN", "unknown", "UnKnOwN"} {
		t.Run(unknownHash, func(t *testing.T) {
			meta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: unknownHash}, bytes.NewReader(testData), int64(len(testData)), nil)
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			// Verify hash was computed
			hasher := sha256.New()
			hasher.Write(testData)
			expectedHash := hex.EncodeToString(hasher.Sum(nil))

			if meta.Hash != expectedHash {
				t.Errorf("Expected hash %s, got %s", expectedHash, meta.Hash)
			}
		})
	}
}

// TestHashComputingArtifactStorageHashComputationCorrectness tests SHA-256 computation correctness
func TestHashComputingArtifactStorageHashComputationCorrectness(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	ctx := context.Background()

	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello")},
		{"medium", []byte("This is a test string with some content")},
		{"large", bytes.Repeat([]byte("a"), 10000)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: ""}, bytes.NewReader(tc.data), int64(len(tc.data)), nil)
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}

			// Compute expected hash
			hasher := sha256.New()
			hasher.Write(tc.data)
			expectedHash := hex.EncodeToString(hasher.Sum(nil))

			if meta.Hash != expectedHash {
				t.Errorf("Hash mismatch: expected %s, got %s", expectedHash, meta.Hash)
			}
		})
	}
}

// TestHashComputingArtifactStorageExistingHash tests handling when computed hash already exists
// func TestHashComputingArtifactStorageExistingHash(t *testing.T) {
// 	baseDir := t.TempDir()
// 	storage, err := NewSimpleFileStorage("test-storage", baseDir)
// 	if err != nil {
// 		t.Fatalf("Failed to create storage: %v", err)
// 	}
//
// 	wrapper := NewHashComputingArtifactStorage(storage)
//
// 	ctx := context.Background()
// 	testData := []byte("test data")
//
// 	// Compute hash first
// 	hasher := sha256.New()
// 	hasher.Write(testData)
// 	computedHash := hex.EncodeToString(hasher.Sum(nil))
//
// 	// Create artifact with known hash first
// 	_, err = wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: computedHash}, bytes.NewReader(testData), int64(len(testData)), nil)
// 	if err != nil {
// 		t.Fatalf("Initial create failed: %v", err)
// 	}
//
// 	// Now create with unknown hash (should detect existing and merge)
// 	meta := &models.ArtifactMeta{
// 		Hash:             "",
// 		Length:           int64(len(testData)),
// 		CreatedTimestamp: time.Now().Unix(),
// 		References: []models.ArtifactReference{
// 			{Name: "ref1", Registry: "registry1", ReferencedTimestamp: time.Now().Unix()},
// 		},
// 	}
//
// 	resultMeta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: ""}, bytes.NewReader(testData), int64(len(testData)), meta)
// 	if err != nil {
// 		t.Fatalf("Create with existing hash failed: %v", err)
// 	}
//
// 	// Should return existing metadata (or merged)
// 	if resultMeta.Hash != computedHash {
// 		t.Errorf("Expected hash %s, got %s", computedHash, resultMeta.Hash)
// 	}
//
// 	// Verify temp file was cleaned up (should not exist)
// 	// Check that no temp-* files exist in baseDir
// 	entries, err := os.ReadDir(baseDir)
// 	if err != nil {
// 		t.Fatalf("Failed to read baseDir: %v", err)
// 	}
//
// 	for _, entry := range entries {
// 		if strings.HasPrefix(entry.Name(), "temp-") {
// 			t.Errorf("Temp file still exists: %s", entry.Name())
// 		}
// 	}
// }

// TestHashComputingArtifactStorageTempFileCleanup tests temp file cleanup on errors
func TestHashComputingArtifactStorageTempFileCleanup(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	ctx := context.Background()
	testData := []byte("test data")

	// Create with unknown hash that will fail during move (simulate by using invalid storage)
	// Actually, we can't easily simulate move failure, so let's test cleanup function directly
	tempHash := wrapper.generateTempHash()

	// Create temp artifact
	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: tempHash}, bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Failed to create temp artifact: %v", err)
	}

	// Verify it exists
	_, err = storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: tempHash})
	if err != nil {
		t.Fatalf("Temp artifact should exist: %v", err)
	}

	// Cleanup
	err = wrapper.cleanupTempHash(ctx, tempHash)
	if err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Verify temp artifact is gone (moved to trash or deleted)
	_, err = storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: tempHash})
	if err == nil {
		// Check if it's in trash
		trashPath := filepath.Join(baseDir, ".trash")
		if _, err := os.Stat(trashPath); err == nil {
			// Check trash directory
			entries, _ := os.ReadDir(trashPath)
			if len(entries) > 0 {
				// It's in trash, which is acceptable
				return
			}
		}
		t.Error("Temp artifact should be cleaned up")
	}
}

// TestHashComputingArtifactStorageDelegation tests that other methods delegate correctly
// func TestHashComputingArtifactStorageDelegation(t *testing.T) {
// 	baseDir := t.TempDir()
// 	storage, err := NewSimpleFileStorage("test-storage", baseDir)
// 	if err != nil {
// 		t.Fatalf("Failed to create storage: %v", err)
// 	}
//
// 	wrapper := NewHashComputingArtifactStorage(storage)
//
// 	ctx := context.Background()
// 	hash := "testhash123"
// 	testData := []byte("test data")
//
// 	// Create artifact
// 	_, err = wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData), int64(len(testData)), nil)
// 	if err != nil {
// 		t.Fatalf("Create failed: %v", err)
// 	}
//
// 	// Test Read delegation
// 	readReq := models.ArtifactRange{
// 		Identifier: models.ArtifactIdentifier{Hash: hash},
// 		Range: models.ByteRange{
// 			Offset: 0,
// 			Length: -1,
// 		},
// 	}
// 	rc, _, err := wrapper.ReadBlob(ctx, readReq)
// 	if err != nil {
// 		t.Fatalf("Read failed: %v", err)
// 	}
// 	rc.Close()
//
// 	// Test GetMeta delegation
// 	meta, err := wrapper.GetMeta(ctx, models.ArtifactIdentifier{Hash: hash})
// 	if err != nil {
// 		t.Fatalf("GetMeta failed: %v", err)
// 	}
// 	if meta.Hash != hash {
// 		t.Errorf("Expected hash %s, got %s", hash, meta.Hash)
// 	}
//
// 	// Test UpdateMeta delegation
// 	meta.References = []models.ArtifactReference{
// 		{Name: "test", Registry: "test", ReferencedTimestamp: time.Now().Unix()},
// 	}
// 	updatedMeta, err := wrapper.UpdateMeta(ctx, *meta)
// 	if err != nil {
// 		t.Fatalf("UpdateMeta failed: %v", err)
// 	}
// 	if len(updatedMeta.References) != 1 {
// 		t.Errorf("Expected 1 reference, got %d", len(updatedMeta.References))
// 	}
//
// 	// Test Delete delegation
// 	ref := updatedMeta.References[0]
// 	_, err = wrapper.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref})
// 	if err != nil {
// 		t.Fatalf("Delete failed: %v", err)
// 	}
// }

// TestHashComputingArtifactStorageConcurrentUnknownHashes tests concurrent creates with unknown hashes
func TestHashComputingArtifactStorageConcurrentUnknownHashes(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	wrapper := NewHashComputingArtifactStorage(storage)

	ctx := context.Background()
	testData := []byte("test data")

	// Multiple goroutines creating with unknown hashes
	const numGoroutines = 5
	errors := make(chan error, numGoroutines)
	hashes := make(chan string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			meta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: ""}, bytes.NewReader(testData), int64(len(testData)), nil)
			if err != nil {
				errors <- err
				return
			}
			hashes <- meta.Hash
		}()
	}

	// Collect results
	collectedHashes := make(map[string]bool)
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errors:
			t.Errorf("Concurrent create error: %v", err)
		case hash := <-hashes:
			collectedHashes[hash] = true
		}
	}

	// All should have the same hash (same data)
	hasher := sha256.New()
	hasher.Write(testData)
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	if len(collectedHashes) != 1 {
		t.Errorf("Expected 1 unique hash, got %d", len(collectedHashes))
	}

	if !collectedHashes[expectedHash] {
		t.Errorf("Expected hash %s not found in results", expectedHash)
	}
}

// TestHashComputingArtifactStorageWithMetadata tests Create with metadata and unknown hash
// func TestHashComputingArtifactStorageWithMetadata(t *testing.T) {
// 	baseDir := t.TempDir()
// 	storage, err := NewSimpleFileStorage("test-storage", baseDir)
// 	if err != nil {
// 		t.Fatalf("Failed to create storage: %v", err)
// 	}
//
// 	wrapper := NewHashComputingArtifactStorage(storage)
//
// 	ctx := context.Background()
// 	testData := []byte("test data")
// 	meta := &models.ArtifactMeta{
// 		Hash:             "",
// 		Length:           int64(len(testData)),
// 		CreatedTimestamp: time.Now().Unix(),
// 		References: []models.ArtifactReference{
// 			{Name: "ref1", Registry: "registry1", ReferencedTimestamp: time.Now().Unix()},
// 		},
// 	}
//
// 	createdMeta, err := wrapper.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: ""}, bytes.NewReader(testData), int64(len(testData)), meta)
// 	if err != nil {
// 		t.Fatalf("Create failed: %v", err)
// 	}
//
// 	// Verify hash was computed
// 	hasher := sha256.New()
// 	hasher.Write(testData)
// 	expectedHash := hex.EncodeToString(hasher.Sum(nil))
//
// 	if createdMeta.Hash != expectedHash {
// 		t.Errorf("Expected hash %s, got %s", expectedHash, createdMeta.Hash)
// 	}
//
// 	// Verify metadata was preserved
// 	if len(createdMeta.References) != 1 {
// 		t.Errorf("Expected 1 reference, got %d", len(createdMeta.References))
// 	}
//
// 	if createdMeta.References[0].Name != "ref1" {
// 		t.Errorf("Expected reference name ref1, got %s", createdMeta.References[0].Name)
// 	}
// }
