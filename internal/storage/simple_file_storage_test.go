package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collective-projects/brm-server/pkg/models"
)

// TestSimpleFileStorageFileSystemStructure tests the git-like directory structure
func TestSimpleFileStorageFileSystemStructure(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "abc123def456" // First two chars: "ab"
	testData := []byte("test data")
	ref := &models.ArtifactReference{
		Name:                "test-ref",
		Registry:            "test-registry",
		ReferencedTimestamp: time.Now().Unix(),
	}

	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref}, bytes.NewReader(testData), int64(len(testData)), nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify directory structure (blob/meta/ref with 2-char subdirs)
	expectedBlobSubdir := filepath.Join(baseDir, "blob", "ab")
	if _, err := os.Stat(expectedBlobSubdir); err != nil {
		t.Errorf("Expected blob subdirectory %s to exist", expectedBlobSubdir)
	}

	expectedBlobFile := filepath.Join(expectedBlobSubdir, "c123def456")
	if _, err := os.Stat(expectedBlobFile); err != nil {
		t.Errorf("Expected blob file %s to exist", expectedBlobFile)
	}

	// Verify metadata file
	expectedMetaSubdir := filepath.Join(baseDir, "meta", "ab")
	if _, err := os.Stat(expectedMetaSubdir); err != nil {
		t.Errorf("Expected meta subdirectory %s to exist", expectedMetaSubdir)
	}

	expectedMetaFile := filepath.Join(expectedMetaSubdir, "c123def456")
	if _, err := os.Stat(expectedMetaFile); err != nil {
		t.Errorf("Expected meta file %s to exist", expectedMetaFile)
	}
}

// TestSimpleFileStorageZeroPadding tests zero padding behavior in Update
func TestSimpleFileStorageZeroPadding(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "zeropad123"
	initialData := []byte("Hello")
	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(initialData), int64(len(initialData)), nil)
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Update at offset beyond current size (should pad with zeros)
	updateData := []byte("World")
	req := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: hash},
		Range: models.ByteRange{
			Offset: 10, // Beyond current size of 5
			Length: int64(len(updateData)),
		},
	}
	err = storage.UpdateBlob(ctx, req, bytes.NewReader(updateData))
	if err != nil {
		t.Fatalf("Update with padding failed: %v", err)
	}

	// Verify the result
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
	expectedLength := 10 + len(updateData)
	if len(readData) != expectedLength {
		t.Errorf("Expected length %d, got %d", expectedLength, len(readData))
	}

	// Verify padding (bytes 5-9 should be zeros)
	for i := 5; i < 10; i++ {
		if readData[i] != 0 {
			t.Errorf("Expected zero padding at offset %d, got %d", i, readData[i])
		}
	}

	// Verify update data is at correct position
	expectedUpdate := readData[10:]
	verifyData(t, expectedUpdate, updateData)
}

// TestSimpleFileStorageMetadataFileExtension tests metadata file naming
func TestSimpleFileStorageMetadataFileExtension(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "metafile123"
	testData := []byte("test")
	ref := &models.ArtifactReference{
		Name:                "test",
		Registry:            "docker:test",
		ReferencedTimestamp: 1234567890,
	}
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: 1234567890,
	}

	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref}, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify metadata file exists with .meta.json extension
	_, _, metaPath := storage.getPaths(hash)
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("Expected metadata file %s to exist", metaPath)
	}
}

// TestSimpleFileStorageMultipleReferences tests creating artifacts with multiple references
func TestSimpleFileStorageMultipleReferences(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "multiref123"
	testData := []byte("test data")

	// Create artifact with first reference
	ref1 := &models.ArtifactReference{Name: "ref1", Registry: "registry1", ReferencedTimestamp: 1234567890}
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: 1234567890,
	}

	createdMeta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref1}, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Add second reference (use -1 for size to skip length validation since artifact already exists)
	ref2 := &models.ArtifactReference{Name: "ref2", Registry: "registry2", ReferencedTimestamp: 1234567891}
	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref2}, nil, -1, createdMeta)
	if err != nil {
		t.Fatalf("Adding second reference failed: %v", err)
	}

	// Verify 2 references exist
	refHashes, err := storage.ListReferenceHashes(ctx, hash)
	if err != nil {
		t.Fatalf("ListReferenceHashes failed: %v", err)
	}
	if len(refHashes) != 2 {
		t.Errorf("Expected 2 references, got %d", len(refHashes))
	}
}

// TestSimpleFileStorageReferenceMerging tests merging references when creating existing artifact
func TestSimpleFileStorageReferenceMerging(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "merge123"
	testData := []byte("test data")

	// Create artifact with first reference
	ref1 := &models.ArtifactReference{Name: "ref1", Registry: "registry1", ReferencedTimestamp: 1234567890}
	meta1 := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: 1234567890,
	}

	createdMeta1, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref1}, bytes.NewReader(testData), int64(len(testData)), meta1)
	if err != nil {
		t.Fatalf("First Create failed: %v", err)
	}

	// Create same artifact again with different reference (should merge, not write data)
	ref2 := &models.ArtifactReference{Name: "ref2", Registry: "registry2", ReferencedTimestamp: 1234567891}
	meta2 := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: 1234567891,
	}

	createdMeta2, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref2}, bytes.NewReader(testData), int64(len(testData)), meta2)
	if err != nil {
		t.Fatalf("Second Create failed: %v", err)
	}

	// Verify 2 references exist
	refHashes, err := storage.ListReferenceHashes(ctx, hash)
	if err != nil {
		t.Fatalf("ListReferenceHashes failed: %v", err)
	}
	if len(refHashes) != 2 {
		t.Errorf("Expected 2 references after merge, got %d", len(refHashes))
	}

	// CreatedTimestamp should be preserved from first creation
	if createdMeta2.CreatedTimestamp != createdMeta1.CreatedTimestamp {
		t.Errorf("CreatedTimestamp should be preserved, got %d, expected %d", createdMeta2.CreatedTimestamp, createdMeta1.CreatedTimestamp)
	}

	// Verify both references exist
	retrievedRef1, err := storage.GetReference(ctx, hash, ref1.Name, ref1.Registry)
	if err != nil {
		t.Fatalf("GetReference ref1 failed: %v", err)
	}
	if retrievedRef1.Name != ref1.Name {
		t.Errorf("Expected ref1 name %s, got %s", ref1.Name, retrievedRef1.Name)
	}

	retrievedRef2, err := storage.GetReference(ctx, hash, ref2.Name, ref2.Registry)
	if err != nil {
		t.Fatalf("GetReference ref2 failed: %v", err)
	}
	if retrievedRef2.Name != ref2.Name {
		t.Errorf("Expected ref2 name %s, got %s", ref2.Name, retrievedRef2.Name)
	}
}

// TestSimpleFileStorageHashConflict tests hash conflict error when sizes don't match
func TestSimpleFileStorageHashConflict(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "conflict123"
	testData1 := []byte("test data 1")

	// Create artifact
	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData1), int64(len(testData1)), nil)
	if err != nil {
		t.Fatalf("First Create failed: %v", err)
	}

	// Try to create with different size (should fail)
	testData2 := []byte("different size data")
	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash}, bytes.NewReader(testData2), int64(len(testData2)), nil)
	if err == nil {
		t.Fatal("Expected hash conflict error")
	}

	// Check if it's a HashConflictError
	hashErr, ok := err.(*models.HashConflictError)
	if !ok {
		t.Fatalf("Expected HashConflictError, got %T: %v", err, err)
	}

	if hashErr.Hash != hash {
		t.Errorf("Expected hash %s, got %s", hash, hashErr.Hash)
	}
	if hashErr.ExistingLength != int64(len(testData1)) {
		t.Errorf("Expected existing length %d, got %d", len(testData1), hashErr.ExistingLength)
	}
	if hashErr.ProvidedLength != int64(len(testData2)) {
		t.Errorf("Expected provided length %d, got %d", len(testData2), hashErr.ProvidedLength)
	}
}

// TestSimpleFileStorageDeleteWithMultipleReferences tests deleting one reference while keeping others
func TestSimpleFileStorageDeleteWithMultipleReferences(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "deletemulti123"
	testData := []byte("test data")

	// Create artifact with first reference
	ref1 := &models.ArtifactReference{Name: "ref1", Registry: "registry1", ReferencedTimestamp: 1234567890}
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: 1234567890,
	}

	createdMeta, err := storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref1}, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Add second reference (use -1 for size to skip length validation since artifact already exists)
	ref2 := &models.ArtifactReference{Name: "ref2", Registry: "registry2", ReferencedTimestamp: 1234567891}
	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref2}, nil, -1, createdMeta)
	if err != nil {
		t.Fatalf("Adding second reference failed: %v", err)
	}

	// Delete one reference
	err = storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref1})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify artifact still exists
	_, artifactPath, _ := storage.getPaths(hash)
	if _, err := os.Stat(artifactPath); os.IsNotExist(err) {
		t.Error("Artifact should still exist after deleting one reference")
	}

	// Verify remaining reference
	refHashes, err := storage.ListReferenceHashes(ctx, hash)
	if err != nil {
		t.Fatalf("ListReferenceHashes failed: %v", err)
	}
	if len(refHashes) != 1 {
		t.Errorf("Expected 1 reference remaining, got %d", len(refHashes))
	}

	// Verify ref2 still exists
	retrievedRef2, err := storage.GetReference(ctx, hash, ref2.Name, ref2.Registry)
	if err != nil {
		t.Fatalf("GetReference ref2 failed: %v", err)
	}
	if retrievedRef2.Name != "ref2" {
		t.Errorf("Expected remaining reference to be ref2, got %s", retrievedRef2.Name)
	}
}

// TestSimpleFileStorageDeleteLastReference tests moving to trash when last reference is deleted
func TestSimpleFileStorageDeleteLastReference(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "deletelast123"
	testData := []byte("test data")

	// Create artifact with one reference
	ref := &models.ArtifactReference{Name: "ref1", Registry: "registry1", ReferencedTimestamp: 1234567890}
	meta := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: 1234567890,
	}

	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref}, bytes.NewReader(testData), int64(len(testData)), meta)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Delete the only reference
	err = storage.DeleteArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify artifact is moved to trash
	_, artifactPath, _ := storage.getPaths(hash)
	if _, err := os.Stat(artifactPath); err == nil {
		t.Error("Artifact should be moved to trash")
	}

	trashDir, trashArtifactPath, _ := storage.getTrashPath(hash)
	if _, err := os.Stat(trashArtifactPath); os.IsNotExist(err) {
		t.Errorf("Artifact should exist in trash at %s", trashArtifactPath)
	}

	// Verify trash directory structure
	if _, err := os.Stat(trashDir); os.IsNotExist(err) {
		t.Errorf("Trash directory should exist at %s", trashDir)
	}
}

// TestSimpleFileStorageReferenceDeduplication tests that duplicate references update timestamp
func TestSimpleFileStorageReferenceDeduplication(t *testing.T) {
	baseDir := t.TempDir()
	storage, err := NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	ctx := context.Background()
	hash := "dedup123"
	testData := []byte("test data")

	// Create artifact with reference
	ref1 := &models.ArtifactReference{Name: "ref1", Registry: "registry1", ReferencedTimestamp: 1234567890}
	meta1 := &models.ArtifactMeta{
		Hash:             hash,
		Length:           int64(len(testData)),
		CreatedTimestamp: 1234567890,
	}

	_, err = storage.CreateArtifact(ctx, models.ArtifactIdentifier{Hash: hash, Reference: ref1}, bytes.NewReader(testData), int64(len(testData)), meta1)
	if err != nil {
		t.Fatalf("First Create failed: %v", err)
	}

	// Update same reference with newer timestamp
	ref2 := &models.ArtifactReference{Name: "ref1", Registry: "registry1", ReferencedTimestamp: 1234567895}
	_, err = storage.UpdateReference(ctx, hash, *ref2)
	if err != nil {
		t.Fatalf("UpdateReference failed: %v", err)
	}

}
