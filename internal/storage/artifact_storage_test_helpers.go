package storage

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/collective-projects/brm-server/pkg/models"
)

// Helper function to create test data
func createTestData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	return data
}

// Helper function to create test metadata
func createTestMeta(hash, name, repo string, length int64) *models.ArtifactMeta {
	now := time.Now().Unix()
	return &models.ArtifactMeta{
		Hash:             hash,
		Length:           length,
		CreatedTimestamp: now,
	}
}

// Helper function to create a test reference
func createTestReference(name, repo string) models.ArtifactReference {
	return models.ArtifactReference{
		Name:                name,
		Registry:            repo,
		ReferencedTimestamp: time.Now().Unix(),
	}
}

// Helper function to read all data from a ReadCloser
func readAllData(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Failed to read data: %v", err)
	}
	return data
}

// Helper function to verify data matches expected
func verifyData(t *testing.T, actual, expected []byte) {
	t.Helper()
	if !bytes.Equal(actual, expected) {
		t.Errorf("Data mismatch: expected %d bytes, got %d bytes", len(expected), len(actual))
		if len(actual) == len(expected) {
			for i := range actual {
				if actual[i] != expected[i] {
					t.Errorf("First mismatch at byte %d: expected %d, got %d", i, expected[i], actual[i])
					break
				}
			}
		}
	}
}

// testArtifactStorageCreate tests the Create method with various scenarios
func testArtifactStorageCreate(t *testing.T, storage models.ArtifactStorage) {
	ctx := context.Background()

	t.Run("create_with_data_only", func(t *testing.T) {
		hash := "abc123def456"
		testData := []byte("Hello, World!")
		r := bytes.NewReader(testData)

		meta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, r, int64(len(testData)), nil)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		if meta == nil {
			t.Fatal("Create returned nil metadata")
		}
		if meta.Hash != hash {
			t.Errorf("Expected hash %s, got %s", hash, meta.Hash)
		}

		// Verify data can be read back
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		verifyData(t, readData, testData)

		if actual.Range.Length != int64(len(testData)) {
			t.Errorf("Expected length %d, got %d", len(testData), actual.Range.Length)
		}
	})

	t.Run("create_with_data_and_metadata", func(t *testing.T) {
		hash := "xyz789uvw012"
		testData := []byte("Test artifact data")
		meta := createTestMeta(hash, "test-artifact", "docker:hub.docker.com", int64(len(testData)))
		ref := createTestReference("test-artifact", "docker:hub.docker.com")
		r := bytes.NewReader(testData)

		createdMeta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref}, r, int64(len(testData)), meta)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		if createdMeta == nil {
			t.Fatal("Create returned nil metadata")
		}

		// Verify metadata
		retrievedMeta, err := storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: hash})
		if err != nil {
			t.Fatalf("GetMeta failed: %v", err)
		}

		// Verify reference was created
		retrievedRef, err := storage.GetReference(ctx, hash, "test-artifact", "docker:hub.docker.com")
		if err != nil {
			t.Fatalf("GetReference failed: %v", err)
		}
		if retrievedRef.Name != "test-artifact" {
			t.Errorf("Expected name test-artifact, got %s", retrievedRef.Name)
		}
		if retrievedMeta.Hash != hash {
			t.Errorf("Expected hash %s, got %s", hash, retrievedMeta.Hash)
		}
		if ref.Registry != "docker:hub.docker.com" {
			t.Errorf("Expected repo docker:hub.docker.com, got %s", ref.Registry)
		}
		if retrievedMeta.Length != int64(len(testData)) {
			t.Errorf("Expected length %d, got %d", len(testData), retrievedMeta.Length)
		}
	})

	t.Run("create_with_empty_data", func(t *testing.T) {
		hash := "empty123"
		testData := []byte{}
		r := bytes.NewReader(testData)

		meta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, r, 0, nil)
		if err != nil {
			t.Fatalf("Create failed with empty data: %v", err)
		}
		if meta == nil {
			t.Fatal("Create returned nil metadata")
		}

		// Verify empty data can be read
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		if len(readData) != 0 {
			t.Errorf("Expected empty data, got %d bytes", len(readData))
		}
		if actual.Range.Length != 0 {
			t.Errorf("Expected length 0, got %d", actual.Range.Length)
		}
	})

	t.Run("create_with_large_data", func(t *testing.T) {
		hash := "large456"
		testData := createTestData(1024 * 1024) // 1MB
		r := bytes.NewReader(testData)

		meta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, r, int64(len(testData)), nil)
		if err != nil {
			t.Fatalf("Create failed with large data: %v", err)
		}
		if meta == nil {
			t.Fatal("Create returned nil metadata")
		}

		// Verify large data can be read back
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		if len(readData) != len(testData) {
			t.Errorf("Expected %d bytes, got %d bytes", len(testData), len(readData))
		}
		if actual.Range.Length != int64(len(testData)) {
			t.Errorf("Expected length %d, got %d", len(testData), actual.Range.Length)
		}
	})
}

// testArtifactStorageRead tests the Read method with different range scenarios
func testArtifactStorageRead(t *testing.T, storage models.ArtifactStorage) {
	ctx := context.Background()

	// Setup: create test artifact
	hash := "readtest123"
	testData := []byte("0123456789ABCDEF")
	_, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	t.Run("read_full_artifact", func(t *testing.T) {
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		verifyData(t, readData, testData)

		if actual.Range.Offset != 0 {
			t.Errorf("Expected offset 0, got %d", actual.Range.Offset)
		}
		if actual.Range.Length != int64(len(testData)) {
			t.Errorf("Expected length %d, got %d", len(testData), actual.Range.Length)
		}
	})

	t.Run("read_partial_from_start", func(t *testing.T) {
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: 5,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		expected := testData[0:5]
		verifyData(t, readData, expected)

		if actual.Range.Offset != 0 {
			t.Errorf("Expected offset 0, got %d", actual.Range.Offset)
		}
		if actual.Range.Length != 5 {
			t.Errorf("Expected length 5, got %d", actual.Range.Length)
		}
	})

	t.Run("read_partial_from_middle", func(t *testing.T) {
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 5,
				Length: 5,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		expected := testData[5:10]
		verifyData(t, readData, expected)

		if actual.Range.Offset != 5 {
			t.Errorf("Expected offset 5, got %d", actual.Range.Offset)
		}
		if actual.Range.Length != 5 {
			t.Errorf("Expected length 5, got %d", actual.Range.Length)
		}
	})

	t.Run("read_partial_from_end", func(t *testing.T) {
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 12,
				Length: -1,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		expected := testData[12:]
		verifyData(t, readData, expected)

		if actual.Range.Offset != 12 {
			t.Errorf("Expected offset 12, got %d", actual.Range.Offset)
		}
		if actual.Range.Length != int64(len(expected)) {
			t.Errorf("Expected length %d, got %d", len(expected), actual.Range.Length)
		}
	})

	t.Run("read_range_exceeds_file_size", func(t *testing.T) {
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: 1000, // Much larger than file size
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		verifyData(t, readData, testData) // Should return only available data

		if actual.Range.Length != int64(len(testData)) {
			t.Errorf("Expected length %d (file size), got %d", len(testData), actual.Range.Length)
		}
	})

	t.Run("read_offset_beyond_file_size", func(t *testing.T) {
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 1000,
				Length: 10,
			},
		}
		rc, actual, err := storage.ReadBlob(ctx, req)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		if len(readData) != 0 {
			t.Errorf("Expected empty data, got %d bytes", len(readData))
		}
		if actual.Range.Length != 0 {
			t.Errorf("Expected length 0, got %d", actual.Range.Length)
		}
	})

	t.Run("read_nonexistent_artifact", func(t *testing.T) {
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: "nonexistent"},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		_, _, err := storage.ReadBlob(ctx, req)
		if err == nil {
			t.Error("Expected error for nonexistent artifact")
		}
	})
}

// testArtifactStorageUpdate tests the Update method with various range updates
func testArtifactStorageUpdate(t *testing.T, storage models.ArtifactStorage) {
	ctx := context.Background()

	t.Run("update_existing_range", func(t *testing.T) {
		hash := "updatetest1"
		initialData := []byte("0123456789")
		_, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(initialData), int64(len(initialData)), nil)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		// Update bytes 2-5
		updateData := []byte("ABC")
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 2,
				Length: 3,
			},
		}
		err = storage.UpdateBlob(ctx, req, bytes.NewReader(updateData))
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		// Verify update
		readReq := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		rc, _, err := storage.ReadBlob(ctx, readReq)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		expected := []byte("01ABC56789")
		verifyData(t, readData, expected)
	})

	t.Run("update_with_append", func(t *testing.T) {
		hash := "updatetest2"
		initialData := []byte("Hello")
		_, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(initialData), int64(len(initialData)), nil)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		// Append data
		appendData := []byte(", World!")
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: int64(len(initialData)),
				Length: -1,
			},
		}
		err = storage.UpdateBlob(ctx, req, bytes.NewReader(appendData))
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		// Verify append
		readReq := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		rc, _, err := storage.ReadBlob(ctx, readReq)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		expected := []byte("Hello, World!")
		verifyData(t, readData, expected)
	})

	t.Run("update_with_specific_length", func(t *testing.T) {
		hash := "updatetest3"
		initialData := []byte("0123456789")
		_, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(initialData), int64(len(initialData)), nil)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		// Update with specific length (shorter than source)
		updateData := []byte("ABCDEFGHIJ")
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 2,
				Length: 4, // Only write 4 bytes
			},
		}
		err = storage.UpdateBlob(ctx, req, bytes.NewReader(updateData))
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		// Verify update
		readReq := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		rc, _, err := storage.ReadBlob(ctx, readReq)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		defer rc.Close()

		readData := readAllData(t, rc)
		expected := []byte("01ABCD6789")
		verifyData(t, readData, expected)
	})
}

// testArtifactStorageDelete tests the Delete method
func testArtifactStorageDelete(t *testing.T, storage models.ArtifactStorage) {
	ctx := context.Background()

	t.Run("delete_existing_artifact", func(t *testing.T) {
		hash := "deletetest1"
		testData := []byte("test data")
		meta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData), int64(len(testData)), nil)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		// Create a reference to delete
		ref := models.ArtifactReference{
			Name:                "test",
			Registry:            "test",
			ReferencedTimestamp: meta.CreatedTimestamp,
		}
		// Create the reference
		_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref}, bytes.NewReader(nil), int64(len(testData)), meta)
		if err != nil {
			t.Fatalf("Failed to create reference: %v", err)
		}

		err = storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref})
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		// Verify deletion
		req := models.ArtifactRange{
			Identifier: models.ArtifactIdentifier{Hash: hash},
			Range: models.ByteRange{
				Offset: 0,
				Length: -1,
			},
		}
		_, _, err = storage.ReadBlob(ctx, req)
		if err == nil {
			t.Error("Expected error when reading deleted artifact")
		}
	})

	t.Run("delete_nonexistent_artifact", func(t *testing.T) {
		ref := models.ArtifactReference{
			Name:                "test",
			Registry:            "test",
			ReferencedTimestamp: time.Now().Unix(),
		}
		err := storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: "nonexistent", Reference: &ref})
		if err == nil {
			t.Error("Delete should error for nonexistent artifact")
		}
	})

	t.Run("delete_with_metadata", func(t *testing.T) {
		hash := "deletetest2"
		testData := []byte("test data")
		meta := createTestMeta(hash, "test", "docker:test", int64(len(testData)))
		ref := createTestReference("test", "docker:test")
		_, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref}, bytes.NewReader(testData), int64(len(testData)), meta)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		err = storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref})
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		// Verify metadata is also deleted
		_, err = storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: hash})
		if err == nil {
			t.Error("Expected error when reading deleted metadata")
		}
	})
}

// testArtifactStorageGetMeta tests the GetMeta method
func testArtifactStorageGetMeta(t *testing.T, storage models.ArtifactStorage) {
	ctx := context.Background()

	t.Run("get_existing_metadata", func(t *testing.T) {
		hash := "metatest1"
		testData := []byte("test")
		meta := createTestMeta(hash, "test-artifact", "docker:hub.docker.com", int64(len(testData)))
		ref := createTestReference("test-artifact", "docker:hub.docker.com")
		_, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref}, bytes.NewReader(testData), int64(len(testData)), meta)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		retrievedMeta, err := storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: hash})
		if err != nil {
			t.Fatalf("GetMeta failed: %v", err)
		}

		// Verify reference was created
		retrievedRef, err := storage.GetReference(ctx, hash, "test-artifact", "docker:hub.docker.com")
		if err != nil {
			t.Fatalf("GetReference failed: %v", err)
		}
		if retrievedRef.Name != "test-artifact" {
			t.Errorf("Expected name test-artifact, got %s", retrievedRef.Name)
		}
		if retrievedMeta.Hash != hash {
			t.Errorf("Expected hash %s, got %s", hash, retrievedMeta.Hash)
		}
		if retrievedRef.Registry != "docker:hub.docker.com" {
			t.Errorf("Expected repo docker:hub.docker.com, got %s", retrievedRef.Registry)
		}
		if retrievedMeta.Length != meta.Length {
			t.Errorf("Expected length %d, got %d", meta.Length, retrievedMeta.Length)
		}
	})

	t.Run("get_nonexistent_metadata", func(t *testing.T) {
		_, err := storage.GetMeta(ctx, models.ArtifactIdentifier{Hash: "nonexistent"})
		if err == nil {
			t.Error("Expected error for nonexistent metadata")
		}
	})
}

// testArtifactStorageUpdateReference tests the UpdateReference method
func testArtifactStorageUpdateReference(t *testing.T, storage models.ArtifactStorage) {
	ctx := context.Background()

	t.Run("update_existing_reference", func(t *testing.T) {
		hash := "reftest1"
		testData := []byte("test")
		initialMeta := createTestMeta(hash, "initial", "docker:test", int64(len(testData)))
		_, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData), int64(len(testData)), initialMeta)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		// Create a reference
		ref := createTestReference("test-ref", "docker:test")
		_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &ref}, bytes.NewReader(nil), int64(len(testData)), initialMeta)
		if err != nil {
			t.Fatalf("Failed to create reference: %v", err)
		}

		// Update the reference
		ref.Description = "Updated description"
		ref.Tags = map[string]string{"env": "test", "version": "1.0"}

		result, err := storage.UpdateReference(ctx, hash, ref)
		if err != nil {
			t.Fatalf("UpdateReference failed: %v", err)
		}

		if result.Description != "Updated description" {
			t.Errorf("Expected description 'Updated description', got %s", result.Description)
		}
		if result.Tags["env"] != "test" {
			t.Errorf("Expected tag env=test, got %s", result.Tags["env"])
		}
		if result.Tags["version"] != "1.0" {
			t.Errorf("Expected tag version=1.0, got %s", result.Tags["version"])
		}
	})
}

// testArtifactStorageFullWorkflow tests a complete workflow combining multiple operations
func testArtifactStorageFullWorkflow(t *testing.T, storage models.ArtifactStorage) {
	ctx := context.Background()

	hash := "workflow123"
	testData := []byte("Initial data")
	meta := createTestMeta(hash, "workflow-artifact", "docker:test", int64(len(testData)))

	// 1. Create artifact with metadata
	createdMeta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if createdMeta == nil {
		t.Fatal("Create returned nil metadata")
	}

	// 2. Read full artifact
	req := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: hash},
		Range: models.ByteRange{
			Offset: 0,
			Length: -1,
		},
	}
	rc, _, err := storage.ReadBlob(ctx, req)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	readData := readAllData(t, rc)
	verifyData(t, readData, testData)

	// 3. Read partial range
	partialReq := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: hash},
		Range: models.ByteRange{
			Offset: 0,
			Length: 7,
		},
	}
	rc, _, err = storage.ReadBlob(ctx, partialReq)
	if err != nil {
		t.Fatalf("Partial read failed: %v", err)
	}
	partialData := readAllData(t, rc)
	expectedPartial := testData[0:7]
	verifyData(t, partialData, expectedPartial)

	// 4. Update artifact range (overwrite first part)
	updateData := []byte("Updated")
	updateReq := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: hash},
		Range: models.ByteRange{
			Offset: 0,
			Length: int64(len(updateData)),
		},
	}
	err = storage.UpdateBlob(ctx, updateReq, bytes.NewReader(updateData))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 5. Add a new reference
	now := time.Now().Unix()
	newRef := models.ArtifactReference{
		Name:                "updated-workflow",
		Registry:            "docker:updated",
		ReferencedTimestamp: now,
	}
	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &newRef}, bytes.NewReader(nil), int64(len(testData)), createdMeta)
	if err != nil {
		t.Fatalf("Failed to add reference: %v", err)
	}

	// 6. Read updated artifact
	rc, _, err = storage.ReadBlob(ctx, req)
	if err != nil {
		t.Fatalf("Read after update failed: %v", err)
	}
	updatedReadData := readAllData(t, rc)
	// Update overwrites the first part, rest remains
	expectedUpdated := make([]byte, len(testData))
	copy(expectedUpdated, updateData)
	copy(expectedUpdated[len(updateData):], testData[len(updateData):])
	verifyData(t, updatedReadData, expectedUpdated)

	// 7. Verify reference was added
	retrievedRef, err := storage.GetReference(ctx, hash, "updated-workflow", "docker:updated")
	if err != nil {
		t.Fatalf("GetReference failed: %v", err)
	}
	if retrievedRef.Name != "updated-workflow" {
		t.Errorf("Expected name updated-workflow, got %s", retrievedRef.Name)
	}

	// 8. Delete artifact
	err = storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: &newRef})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 9. Verify deletion
	_, _, err = storage.ReadBlob(ctx, req)
	if err == nil {
		t.Error("Expected error when reading deleted artifact")
	}
}
