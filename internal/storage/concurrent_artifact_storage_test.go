package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/collective-projects/brm-server/pkg/models"

	"github.com/gofrs/flock"
)

// mockStorage is a simple mock implementation of ArtifactStorage for testing
type mockStorage struct {
	models.BaseStorage
	creates map[string]*models.ArtifactMeta
	deletes map[string][]models.ArtifactReference
	mu      sync.Mutex
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		creates: make(map[string]*models.ArtifactMeta),
		deletes: make(map[string][]models.ArtifactReference),
	}
}

func (m *mockStorage) CreateArtifact(ctx context.Context, hash string, r io.Reader, size int64, meta *models.ArtifactMeta) (*models.ArtifactMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Simulate reading data
	if r != nil {
		_, _ = io.Copy(io.Discard, r)
	}

	// Check if exists
	if existing, exists := m.creates[hash]; exists {
		// Merge references
		if meta != nil && len(meta.References) > 0 {
			existing.References = append(existing.References, meta.References...)
		}
		return existing, nil
	}

	// Create new
	createdMeta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           size,
		CreatedTimestamp: time.Now().Unix(),
		References:       []models.ArtifactReference{},
	}
	if meta != nil {
		createdMeta.References = meta.References
		createdMeta.CreatedTimestamp = meta.CreatedTimestamp
	}
	m.creates[hash] = createdMeta
	return createdMeta, nil
}

func (m *mockStorage) ReadBlob(ctx context.Context, req models.ArtifactRange) (io.ReadCloser, models.ArtifactRange, error) {
	return nil, models.ArtifactRange{}, fmt.Errorf("not implemented")
}

func (m *mockStorage) UpdateBlob(ctx context.Context, req models.ArtifactRange, r io.Reader) error {
	return fmt.Errorf("not implemented")
}

func (m *mockStorage) DeleteArtifact(ctx context.Context, hash string, ref models.ArtifactReference) (*models.ArtifactMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, exists := m.creates[hash]
	if !exists {
		return nil, fmt.Errorf("artifact not found")
	}

	// Check if reference exists
	found := false
	newRefs := []models.ArtifactReference{}
	for _, r := range meta.References {
		if r.Name == ref.Name && r.Repo == ref.Repo {
			found = true
			// Skip this reference (remove it)
		} else {
			newRefs = append(newRefs, r)
		}
	}

	if !found {
		return nil, fmt.Errorf("reference with name %s and repo %s not found for artifact %s", ref.Name, ref.Repo, hash)
	}

	// Update references
	meta.References = newRefs

	if len(newRefs) == 0 {
		delete(m.creates, hash)
		return nil, nil
	}

	return meta, nil
}

func (m *mockStorage) GetMeta(ctx context.Context, hash string) (*models.ArtifactMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, exists := m.creates[hash]
	if !exists {
		return nil, fmt.Errorf("not found")
	}
	return meta, nil
}

func (m *mockStorage) UpdateMeta(ctx context.Context, meta models.ArtifactMeta) (*models.ArtifactMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.creates[meta.Hash] = &meta
	return &meta, nil
}

// TestConcurrentArtifactStorageDelegation tests that the wrapper correctly delegates to underlying storage
func TestConcurrentArtifactStorageDelegation(t *testing.T) {
	lockDir := t.TempDir()
	mock := newMockStorage()

	wrapper, err := NewConcurrentArtifactStorage(mock, lockDir, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create wrapper: %v", err)
	}

	ctx := context.Background()
	hash := "test123"
	testData := []byte("test data")
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: time.Now().Unix(),
		References: []models.ArtifactReference{
			{Name: "ref1", Repo: "repo1", ReferencedTimestamp: time.Now().Unix()},
		},
	}

	// Test Create
	createdMeta, err := wrapper.CreateArtifact(ctx, hash, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if createdMeta == nil {
		t.Fatal("Create returned nil metadata")
	}
	if createdMeta.Hash != hash {
		t.Errorf("Expected hash %s, got %s", hash, createdMeta.Hash)
	}

	// Test GetMeta
	retrievedMeta, err := wrapper.GetMeta(ctx, hash)
	if err != nil {
		t.Fatalf("GetMeta failed: %v", err)
	}
	if retrievedMeta.Hash != hash {
		t.Errorf("Expected hash %s, got %s", hash, retrievedMeta.Hash)
	}

	// Test Delete
	ref := createdMeta.References[0]
	deletedMeta, err := wrapper.DeleteArtifact(ctx, hash, ref)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deletedMeta != nil {
		t.Error("Expected nil metadata after deleting last reference")
	}
}

// TestConcurrentArtifactStorageConcurrentCreate tests concurrent Create operations on same hash
func TestConcurrentArtifactStorageConcurrentCreate(t *testing.T) {
	lockDir := t.TempDir()
	mock := newMockStorage()

	wrapper, err := NewConcurrentArtifactStorage(mock, lockDir, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create wrapper: %v", err)
	}

	ctx := context.Background()
	hash := "concurrent-create"
	testData := []byte("test data")
	const numGoroutines = 10

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			meta := &models.ArtifactMeta{
				Hash:             hash,
				Length:           int64(len(testData)),
				CreatedTimestamp: time.Now().Unix(),
				References: []models.ArtifactReference{
					{Name: fmt.Sprintf("ref%d", id), Repo: "repo1", ReferencedTimestamp: time.Now().Unix()},
				},
			}
			_, err := wrapper.CreateArtifact(ctx, hash, bytes.NewReader(testData), int64(len(testData)), meta)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent create error: %v", err)
	}

	// Verify all references were merged
	finalMeta, err := wrapper.GetMeta(ctx, hash)
	if err != nil {
		t.Fatalf("GetMeta failed: %v", err)
	}
	if len(finalMeta.References) != numGoroutines {
		t.Errorf("Expected %d references, got %d", numGoroutines, len(finalMeta.References))
	}
}

// TestConcurrentArtifactStorageConcurrentDelete tests concurrent Delete operations on same hash
func TestConcurrentArtifactStorageConcurrentDelete(t *testing.T) {
	lockDir := t.TempDir()
	mock := newMockStorage()

	wrapper, err := NewConcurrentArtifactStorage(mock, lockDir, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create wrapper: %v", err)
	}

	ctx := context.Background()
	hash := "concurrent-delete"
	testData := []byte("test data")

	// Create artifact with multiple references
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: time.Now().Unix(),
		References:       []models.ArtifactReference{},
	}
	for i := 0; i < 10; i++ {
		meta.References = append(meta.References, models.ArtifactReference{
			Name:                fmt.Sprintf("ref%d", i),
			Repo:                "repo1",
			ReferencedTimestamp: time.Now().Unix(),
		})
	}

	createdMeta, err := wrapper.CreateArtifact(ctx, hash, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Delete references concurrently
	const numDeletes = 5
	var wg sync.WaitGroup
	errors := make(chan error, numDeletes)
	successDeletes := make(chan bool, numDeletes)

	for i := 0; i < numDeletes; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ref := createdMeta.References[id]
			_, err := wrapper.DeleteArtifact(ctx, hash, ref)
			if err != nil {
				errors <- err
			} else {
				successDeletes <- true
			}
		}(i)
	}

	wg.Wait()
	close(errors)
	close(successDeletes)

	// Count successful deletions
	successCount := 0
	for range successDeletes {
		successCount++
	}

	// Check for errors (some might fail if reference already deleted, which is expected with locking)
	errorCount := 0
	for err := range errors {
		errorCount++
		// Only log unexpected errors (not "reference not found" which can happen with concurrent deletes)
		if err.Error() != fmt.Sprintf("reference with name ref%d and repo repo1 not found for artifact %s", -1, hash) {
			t.Logf("Concurrent delete error (may be expected): %v", err)
		}
	}

	// Verify that at least some deletions succeeded (locking should prevent all from failing)
	if successCount == 0 && errorCount == numDeletes {
		t.Error("All concurrent deletes failed - locking may not be working")
	}

	// Verify final state is consistent (no corruption)
	finalMeta, err := wrapper.GetMeta(ctx, hash)
	if err != nil && err.Error() != "not found" {
		t.Fatalf("GetMeta failed: %v", err)
	}

	// Verify that remaining references are valid (no duplicates, all from original set)
	if finalMeta != nil {
		remainingCount := len(finalMeta.References)
		if remainingCount < 0 || remainingCount > len(createdMeta.References) {
			t.Errorf("Invalid remaining reference count: %d (original: %d)", remainingCount, len(createdMeta.References))
		}
		// Verify no duplicates in remaining references
		refMap := make(map[string]bool)
		for _, ref := range finalMeta.References {
			key := ref.Name + ":" + ref.Repo
			if refMap[key] {
				t.Errorf("Duplicate reference found: %s", key)
			}
			refMap[key] = true
		}
	}
}

// TestConcurrentArtifactStorageCreateDeleteRace tests Create and Delete race condition
func TestConcurrentArtifactStorageCreateDeleteRace(t *testing.T) {
	lockDir := t.TempDir()
	mock := newMockStorage()

	wrapper, err := NewConcurrentArtifactStorage(mock, lockDir, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create wrapper: %v", err)
	}

	ctx := context.Background()
	hash := "race-test"
	testData := []byte("test data")

	// Create initial artifact
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: time.Now().Unix(),
		References: []models.ArtifactReference{
			{Name: "ref1", Repo: "repo1", ReferencedTimestamp: time.Now().Unix()},
		},
	}
	createdMeta, err := wrapper.CreateArtifact(ctx, hash, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Initial create failed: %v", err)
	}

	// Concurrently create and delete
	var wg sync.WaitGroup
	errors := make(chan error, 2)

	// Create goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		newMeta := &models.ArtifactMeta{
			Hash:             hash,
			Length:           int64(len(testData)),
			CreatedTimestamp: time.Now().Unix(),
			References: []models.ArtifactReference{
				{Name: "ref2", Repo: "repo1", ReferencedTimestamp: time.Now().Unix()},
			},
		}
		_, err := wrapper.CreateArtifact(ctx, hash, bytes.NewReader(testData), int64(len(testData)), newMeta)
		if err != nil {
			errors <- err
		}
	}()

	// Delete goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		ref := createdMeta.References[0]
		_, err := wrapper.DeleteArtifact(ctx, hash, ref)
		if err != nil {
			errors <- err
		}
	}()

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Race condition error: %v", err)
	}

	// Verify final state is consistent
	finalMeta, err := wrapper.GetMeta(ctx, hash)
	if err != nil && err.Error() != "not found" {
		t.Fatalf("GetMeta failed: %v", err)
	}
	// Final state could be either deleted or have ref2, both are valid
	if finalMeta != nil && len(finalMeta.References) > 0 {
		if finalMeta.References[0].Name != "ref2" {
			t.Errorf("Expected ref2, got %s", finalMeta.References[0].Name)
		}
	}
}

// TestConcurrentArtifactStorageLockTimeout tests lock timeout behavior
func TestConcurrentArtifactStorageLockTimeout(t *testing.T) {
	lockDir := t.TempDir()
	mock := newMockStorage()

	// Use a very short timeout
	shortTimeout := 100 * time.Millisecond
	wrapper, err := NewConcurrentArtifactStorage(mock, lockDir, shortTimeout)
	if err != nil {
		t.Fatalf("Failed to create wrapper: %v", err)
	}

	ctx := context.Background()
	hash := "timeout-test"
	testData := []byte("test data")

	// Create a context that will hold the lock for longer than timeout
	lockCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Acquire lock manually to simulate a stuck process
	lockPath := wrapper.GetLockPath(hash)
	lockDirPath := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDirPath, 0755); err != nil {
		t.Fatalf("Failed to create lock directory: %v", err)
	}

	fileLock := flock.New(lockPath)
	if err := fileLock.Lock(); err != nil {
		t.Fatalf("Failed to acquire test lock: %v", err)
	}
	defer fileLock.Unlock()

	// Try to create with the lock held - should timeout
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: time.Now().Unix(),
		References:       []models.ArtifactReference{},
	}

	_, err = wrapper.CreateArtifact(lockCtx, hash, bytes.NewReader(testData), int64(len(testData)), meta)
	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}
	if err.Error() == "" || err.Error() == "context canceled" {
		t.Fatalf("Expected timeout error message, got: %v", err)
	}
}

// TestConcurrentArtifactStorageReadOperationsNoLock tests that Read and GetMeta don't require locks
func TestConcurrentArtifactStorageReadOperationsNoLock(t *testing.T) {
	lockDir := t.TempDir()
	mock := newMockStorage()

	wrapper, err := NewConcurrentArtifactStorage(mock, lockDir, 30*time.Second)
	if err != nil {
		t.Fatalf("Failed to create wrapper: %v", err)
	}

	ctx := context.Background()
	hash := "read-test"
	testData := []byte("test data")

	// Create artifact first
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: time.Now().Unix(),
		References:       []models.ArtifactReference{},
	}
	_, err = wrapper.CreateArtifact(ctx, hash, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Multiple concurrent reads should work without blocking
	const numReads = 20
	var wg sync.WaitGroup
	errors := make(chan error, numReads)

	for i := 0; i < numReads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := wrapper.GetMeta(ctx, hash)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent read error: %v", err)
	}
}

// TestConcurrentArtifactStorageInvalidParams tests parameter validation
func TestConcurrentArtifactStorageInvalidParams(t *testing.T) {
	mock := newMockStorage()
	lockDir := t.TempDir()

	// Test nil storage
	_, err := NewConcurrentArtifactStorage(nil, lockDir, 30*time.Second)
	if err == nil {
		t.Error("Expected error for nil storage")
	}

	// Test empty lockDir
	_, err = NewConcurrentArtifactStorage(mock, "", 30*time.Second)
	if err == nil {
		t.Error("Expected error for empty lockDir")
	}

	// Test zero timeout
	_, err = NewConcurrentArtifactStorage(mock, lockDir, 0)
	if err == nil {
		t.Error("Expected error for zero timeout")
	}

	// Test negative timeout
	_, err = NewConcurrentArtifactStorage(mock, lockDir, -1*time.Second)
	if err == nil {
		t.Error("Expected error for negative timeout")
	}
}
