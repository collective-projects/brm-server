package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/collective-projects/brm-server/pkg/configkeys"
	"github.com/collective-projects/brm-server/pkg/models"
)

// setupTestStorage creates an ArtifactStorage instance via StorageManager for testing
func setupTestStorage(t *testing.T) (models.ArtifactStorage, string) {
	t.Helper()
	baseDir := t.TempDir()
	manager := GetManager()

	// Use a unique alias for each test to avoid conflicts
	// Convert test name to valid DNS name (lowercase, replace invalid chars)
	testName := strings.ToLower(t.Name())
	testName = strings.ReplaceAll(testName, "test", "")
	testName = strings.ReplaceAll(testName, "artifact", "")
	testName = strings.ReplaceAll(testName, "storage", "")
	// Remove any remaining invalid characters
	var builder strings.Builder
	for _, r := range testName {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			builder.WriteRune(r)
		}
	}
	testName = builder.String()
	if len(testName) == 0 {
		testName = "test"
	}
	alias := "test-" + testName
	// Ensure it's a valid DNS name (max 253 chars, start with alphanumeric)
	if len(alias) > 253 {
		alias = alias[:253]
	}
	if len(alias) == 0 || (alias[0] < 'a' || alias[0] > 'z') {
		alias = "test-" + alias
	}

	storage, err := manager.Create(configkeys.StorageClassStdFile, alias, baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	return storage, baseDir
}

func TestArtifactStorageCreate(t *testing.T) {
	storage, _ := setupTestStorage(t)
	testArtifactStorageCreate(t, storage)
}

func TestArtifactStorageRead(t *testing.T) {
	storage, _ := setupTestStorage(t)
	testArtifactStorageRead(t, storage)
}

func TestArtifactStorageUpdate(t *testing.T) {
	storage, _ := setupTestStorage(t)
	testArtifactStorageUpdate(t, storage)
}

func TestArtifactStorageDelete(t *testing.T) {
	storage, _ := setupTestStorage(t)
	testArtifactStorageDelete(t, storage)
}

func TestArtifactStorageGetMeta(t *testing.T) {
	storage, _ := setupTestStorage(t)
	testArtifactStorageGetMeta(t, storage)
}

func TestArtifactStorageUpdateMeta(t *testing.T) {
	storage, _ := setupTestStorage(t)
	testArtifactStorageUpdateMeta(t, storage)
}

func TestArtifactStorageFullWorkflow(t *testing.T) {
	storage, _ := setupTestStorage(t)
	testArtifactStorageFullWorkflow(t, storage)
}

func TestArtifactStorageConcurrent(t *testing.T) {
	storage, _ := setupTestStorage(t)
	ctx := context.Background()

	t.Run("concurrent_create", func(t *testing.T) {
		const numGoroutines = 10
		done := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				hash := fmt.Sprintf("concurrent-%03d", id)
				testData := []byte("test data")
				_, err := storage.Create(ctx, hash, bytes.NewReader(testData), int64(len(testData)), nil)
				done <- err
			}(i)
		}

		for i := 0; i < numGoroutines; i++ {
			if err := <-done; err != nil {
				t.Errorf("Concurrent create failed: %v", err)
			}
		}
	})

	t.Run("concurrent_read", func(t *testing.T) {
		hash := "concurrent-read"
		testData := []byte("test data for concurrent read")
		_, err := storage.Create(ctx, hash, bytes.NewReader(testData), int64(len(testData)), nil)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}

		const numGoroutines = 10
		done := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				req := models.ArtifactRange{
					Hash: hash,
					Range: models.ByteRange{
						Offset: 0,
						Length: -1,
					},
				}
				rc, _, err := storage.Read(ctx, req)
				if err != nil {
					done <- err
					return
				}
				defer rc.Close()
				data, err := io.ReadAll(rc)
				if err != nil {
					done <- err
					return
				}
				if len(data) != len(testData) {
					done <- fmt.Errorf("data length mismatch")
					return
				}
				done <- nil
			}()
		}

		for i := 0; i < numGoroutines; i++ {
			if err := <-done; err != nil {
				t.Errorf("Concurrent read failed: %v", err)
			}
		}
	})

	t.Run("concurrent_mixed_operations", func(t *testing.T) {
		const numArtifacts = 5
		hashes := make([]string, numArtifacts)
		for i := 0; i < numArtifacts; i++ {
			hashes[i] = fmt.Sprintf("mixed-%03d", i)
		}

		// Create artifacts concurrently
		createDone := make(chan error, numArtifacts)
		for i, hash := range hashes {
			go func(h string, id int) {
				testData := []byte("test")
				_, err := storage.Create(ctx, h, bytes.NewReader(testData), int64(len(testData)), nil)
				createDone <- err
			}(hash, i)
		}

		for i := 0; i < numArtifacts; i++ {
			if err := <-createDone; err != nil {
				t.Errorf("Concurrent create failed: %v", err)
			}
		}

		// Read artifacts concurrently
		readDone := make(chan error, numArtifacts)
		for _, hash := range hashes {
			go func(h string) {
				req := models.ArtifactRange{
					Hash: h,
					Range: models.ByteRange{
						Offset: 0,
						Length: -1,
					},
				}
				rc, _, err := storage.Read(ctx, req)
				if err != nil {
					readDone <- err
					return
				}
				defer rc.Close()
				_, err = io.ReadAll(rc)
				readDone <- err
			}(hash)
		}

		for i := 0; i < numArtifacts; i++ {
			if err := <-readDone; err != nil {
				t.Errorf("Concurrent read failed: %v", err)
			}
		}
	})
}
