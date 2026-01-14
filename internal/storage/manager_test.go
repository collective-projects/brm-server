package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/collective-projects/brm-server/pkg/models"
)

func TestStorageManagerCreate(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	manager := GetManager()

	t.Run("create_with_valid_dns_alias", func(t *testing.T) {
		storage, err := manager.Create("std.filestorage", "valid-storage", baseDir)
		if err != nil {
			t.Fatalf("Failed to create storage with valid alias: %v", err)
		}

		// Verify storage works
		hash := "test123"
		testData := []byte("test")
		_, err = storage.Create(ctx, hash, bytes.NewReader(testData), int64(len(testData)), nil)
		if err != nil {
			t.Fatalf("Storage operation failed: %v", err)
		}
	})

	t.Run("create_with_invalid_dns_alias", func(t *testing.T) {
		_, err := manager.Create("std.filestorage", "Invalid-Storage", baseDir)
		if err == nil {
			t.Error("Expected error for invalid DNS alias")
		}
	})

	t.Run("create_with_duplicate_alias", func(t *testing.T) {
		alias := "duplicate-test"
		_, err := manager.Create("std.filestorage", alias, baseDir)
		if err != nil {
			t.Fatalf("First create failed: %v", err)
		}

		_, err = manager.Create("std.filestorage", alias, baseDir)
		if err == nil {
			t.Error("Expected error for duplicate alias")
		}
	})

	t.Run("create_with_nonexistent_class", func(t *testing.T) {
		_, err := manager.Create("nonexistent.class", "test", baseDir)
		if err == nil {
			t.Error("Expected error for nonexistent storage class")
		}
	})
}

func TestStorageManagerGetManager(t *testing.T) {
	// Test that GetManager returns a singleton
	manager1 := GetManager()
	manager2 := GetManager()

	if manager1 != manager2 {
		t.Error("GetManager should return the same instance (singleton)")
	}
}

func TestStorageManagerRegisterFactory(t *testing.T) {
	manager := GetManager()
	baseDir := t.TempDir()

	// Create a test factory that returns a valid storage
	testFactory := func(params ...interface{}) (models.ArtifactStorage, error) {
		if len(params) == 0 {
			return nil, fmt.Errorf("test factory requires baseDir parameter")
		}
		basePath, ok := params[0].(string)
		if !ok {
			return nil, fmt.Errorf("test factory basePath must be a string")
		}
		return NewSimpleFileStorage("test-storage", basePath)
	}

	manager.RegisterFactory("test.factory", testFactory)

	// Verify factory was registered by trying to create with it
	storage, err := manager.Create("test.factory", "test-alias", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage with registered factory: %v", err)
	}
	if storage == nil {
		t.Error("Expected non-nil storage instance")
	}
}

func TestStorageManagerConcurrentFileStorage(t *testing.T) {
	manager := GetManager()
	baseDir := t.TempDir()
	lockDir := t.TempDir()
	lockTimeout := 30 * time.Second

	// Create concurrent file storage via manager
	storage, err := manager.Create("concurrent.filestorage", "concurrent-test", baseDir, lockDir, lockTimeout)
	if err != nil {
		t.Fatalf("Failed to create concurrent storage: %v", err)
	}
	if storage == nil {
		t.Fatal("Expected non-nil storage instance")
	}

	// Verify it's actually a ConcurrentArtifactStorage by testing it works
	ctx := context.Background()
	hash := "test123"
	testData := []byte("test data")

	meta, err := storage.Create(ctx, hash, bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if meta == nil {
		t.Fatal("Create returned nil metadata")
	}
	if meta.Hash != hash {
		t.Errorf("Expected hash %s, got %s", hash, meta.Hash)
	}
}

func TestStorageManagerHashComputingFileStorage(t *testing.T) {
	manager := GetManager()
	baseDir := t.TempDir()

	// Create hash computing file storage via manager (simple version)
	storage, err := manager.Create("hashcomputing.filestorage", "hashcomputing-test", baseDir)
	if err != nil {
		t.Fatalf("Failed to create hash computing storage: %v", err)
	}
	if storage == nil {
		t.Fatal("Expected non-nil storage instance")
	}

	// Verify it computes hashes automatically
	ctx := context.Background()
	testData := []byte("test data")

	// Create with unknown hash (empty string)
	meta, err := storage.Create(ctx, "", bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if meta == nil {
		t.Fatal("Create returned nil metadata")
	}

	// Verify hash was computed (should be SHA-256, not empty)
	if meta.Hash == "" {
		t.Error("Expected computed hash, got empty string")
	}
	if len(meta.Hash) < 3 {
		t.Errorf("Expected hash length >= 3, got %d", len(meta.Hash))
	}

	// Verify we can read back using computed hash
	readReq := models.ArtifactRange{
		Hash: meta.Hash,
		Range: models.ByteRange{
			Offset: 0,
			Length: -1,
		},
	}
	rc, _, err := storage.Read(ctx, readReq)
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
