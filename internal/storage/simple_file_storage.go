package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/collective-projects/brm-server/pkg/models"
)

// SimpleFileStorage implements models.ArtifactStorage
type SimpleFileStorage struct {
	models.BaseStorage
	baseDir string
}

// NewSimpleFileStorage creates a new storage instance and ensures the base directory exists.
func NewSimpleFileStorage(alias, baseDir string) (*SimpleFileStorage, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}
	s := &SimpleFileStorage{
		baseDir: baseDir,
	}
	s.BaseStorage.SetAlias(alias)
	return s, nil
}

// computeReferenceHash computes SHA256 of "Repo::Name" for reference-based storage.
func computeReferenceHash(ref *models.ArtifactReference) string {
	input := ref.Repo + "::" + ref.Name
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

// getPaths returns the directory, artifact path, and metadata path for a given hash.
// Uses blob/ and meta/ subdirectories with 2-character git-like structure.
func (s *SimpleFileStorage) getPaths(hash string) (dir, blobPath, metaPath string) {
	if len(hash) < 2 {
		dir = filepath.Join(s.baseDir, "blob")
		blobPath = filepath.Join(dir, hash+".blob")
		metaPath = filepath.Join(s.baseDir, "meta", hash+".meta")
	} else {
		subDir := hash[:2]
		fileName := hash[2:]
		dir = filepath.Join(s.baseDir, "blob", subDir)
		blobPath = filepath.Join(dir, fileName+".blob")
		metaPath = filepath.Join(s.baseDir, "meta", subDir, fileName+".meta")
	}
	return
}

// getRefPath returns the reference file path for a given reference hash.
func (s *SimpleFileStorage) getRefPath(refHash string) (dir, refPath string) {
	if len(refHash) < 2 {
		dir = filepath.Join(s.baseDir, "ref")
		refPath = filepath.Join(dir, refHash+".ref")
	} else {
		subDir := refHash[:2]
		fileName := refHash[2:]
		dir = filepath.Join(s.baseDir, "ref", subDir)
		refPath = filepath.Join(dir, fileName+".ref")
	}
	return
}

// resolveIdentifier resolves an ArtifactIdentifier to a hash.
// If it has a hash, returns it directly. If it has a reference, looks up the ref file.
func (s *SimpleFileStorage) resolveIdentifier(id models.ArtifactIdentifier) (string, error) {
	if id.HasHash() {
		return id.Hash, nil
	}
	if id.HasReference() {
		refHash := computeReferenceHash(id.Reference)
		_, refPath := s.getRefPath(refHash)

		// Read hash from text file
		data, err := os.ReadFile(refPath)
		if err != nil {
			return "", fmt.Errorf("reference not found: %w", err)
		}
		hash := strings.TrimSpace(string(data))
		return hash, nil
	}
	return "", fmt.Errorf("invalid identifier: neither hash nor reference")
}

// createOrUpdateRef creates or updates a reference file pointing to a hash.
func (s *SimpleFileStorage) createOrUpdateRef(ref *models.ArtifactReference, hash string) error {
	refHash := computeReferenceHash(ref)
	refDir, refPath := s.getRefPath(refHash)

	if err := os.MkdirAll(refDir, 0755); err != nil {
		return fmt.Errorf("failed to create ref directory: %w", err)
	}

	// Write hash to text file
	return os.WriteFile(refPath, []byte(hash), 0644)
}

// deleteRef removes a reference file/link.
func (s *SimpleFileStorage) deleteRef(ref *models.ArtifactReference) error {
	refHash := computeReferenceHash(ref)
	_, refPath := s.getRefPath(refHash)
	err := os.Remove(refPath)
	if os.IsNotExist(err) {
		return nil // Already deleted
	}
	return err
}

// getTrashPath returns the trash directory path for a given hash using git-like structure.
func (s *SimpleFileStorage) getTrashPath(hash string) (dir, blobPath, metaPath string) {
	if len(hash) < 2 {
		dir = filepath.Join(s.baseDir, ".trash", "blob")
		blobPath = filepath.Join(dir, hash+".blob")
		metaPath = filepath.Join(s.baseDir, ".trash", "meta", hash+".meta")
	} else {
		subDir := hash[:2]
		fileName := hash[2:]
		dir = filepath.Join(s.baseDir, ".trash", "blob", subDir)
		blobPath = filepath.Join(dir, fileName+".blob")
		metaPath = filepath.Join(s.baseDir, ".trash", "meta", subDir, fileName+".meta")
	}
	return
}

// mergeReferences merges new references into existing references, deduplicating by Name+Repo.
// If a reference with the same Name+Repo exists, updates ReferencedTimestamp to the latest.
func mergeReferences(existing, new []models.ArtifactReference) []models.ArtifactReference {
	result := make([]models.ArtifactReference, 0, len(existing)+len(new))
	refMap := make(map[string]int) // key: "name:repo", value: index in result

	// Add existing references to map
	for _, ref := range existing {
		key := ref.Name + ":" + ref.Repo
		if idx, exists := refMap[key]; exists {
			// Update timestamp if new one is later
			if ref.ReferencedTimestamp > result[idx].ReferencedTimestamp {
				result[idx].ReferencedTimestamp = ref.ReferencedTimestamp
			}
		} else {
			result = append(result, ref)
			refMap[key] = len(result) - 1
		}
	}

	// Add new references
	for _, ref := range new {
		key := ref.Name + ":" + ref.Repo
		if idx, exists := refMap[key]; exists {
			// Update timestamp if new one is later
			if ref.ReferencedTimestamp > result[idx].ReferencedTimestamp {
				result[idx].ReferencedTimestamp = ref.ReferencedTimestamp
			}
		} else {
			result = append(result, ref)
			refMap[key] = len(result) - 1
		}
	}

	return result
}

// moveToTrash moves an artifact and its metadata to the trash directory.
func (s *SimpleFileStorage) moveToTrash(ctx context.Context, hash string) error {
	_, srcBlobPath, srcMetaPath := s.getPaths(hash)
	trashDir, destBlobPath, destMetaPath := s.getTrashPath(hash)

	// Create trash directory structure
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		return fmt.Errorf("failed to create trash directory: %w", err)
	}
	metaDir := filepath.Dir(destMetaPath)
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		return fmt.Errorf("failed to create trash meta directory: %w", err)
	}

	// Move artifact file
	if err := os.Rename(srcBlobPath, destBlobPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to move artifact to trash: %w", err)
	}

	// Move metadata file (if it exists)
	if err := os.Rename(srcMetaPath, destMetaPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to move metadata to trash: %w", err)
	}

	return nil
}

// CreateArtifact stores the artifact and optional metadata.
// If artifact already exists, validates length and merges references without writing data.
func (s *SimpleFileStorage) CreateArtifact(ctx context.Context, id models.ArtifactIdentifier, r io.Reader, size int64, meta *models.ArtifactMeta) (*models.ArtifactMeta, error) {
	// Resolve identifier to hash
	var hash string
	if id.HasHash() {
		hash = id.Hash
	} else if id.HasReference() {
		// For new artifacts with reference, we need the hash from metadata
		if meta == nil || meta.Hash == "" {
			return nil, fmt.Errorf("creating artifact by reference requires metadata with hash")
		}
		hash = meta.Hash
	} else {
		return nil, fmt.Errorf("invalid identifier")
	}

	dir, blobPath, metaPath := s.getPaths(hash)

	// Check if artifact file already exists
	_, err := os.Stat(blobPath)
	artifactExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to check artifact existence: %w", err)
	}

	if artifactExists {
		// Artifact exists: read existing metadata, validate length, merge references
		existingMeta, err := s.GetMeta(ctx, id)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read existing metadata: %w", err)
		}

		// If metadata doesn't exist, create a basic one from file stats
		if existingMeta == nil {
			stat, err := os.Stat(blobPath)
			if err != nil {
				return nil, fmt.Errorf("failed to stat existing artifact: %w", err)
			}
			existingMeta = &models.ArtifactMeta{
				Hash:             hash,
				Length:           stat.Size(),
				CreatedTimestamp: stat.ModTime().Unix(),
				References:       []models.ArtifactReference{},
			}
		}

		// Validate length if size is provided and not -1
		if size != -1 && size != existingMeta.Length {
			return nil, &models.HashConflictError{
				Hash:           hash,
				ExistingLength: existingMeta.Length,
				ProvidedLength: size,
			}
		}

		// Merge references if meta is provided
		if meta != nil && len(meta.References) > 0 {
			existingMeta.References = mergeReferences(existingMeta.References, meta.References)

			// Create/update reference links for new references
			for _, ref := range meta.References {
				if err := s.createOrUpdateRef(&ref, hash); err != nil {
					return nil, fmt.Errorf("failed to create reference link: %w", err)
				}
			}
		}

		// Update metadata file
		metaDir := filepath.Dir(metaPath)
		if err := os.MkdirAll(metaDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create meta subdirectory: %w", err)
		}

		metaFile, err := os.Create(metaPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create metadata file: %w", err)
		}
		defer metaFile.Close()

		if err := json.NewEncoder(metaFile).Encode(existingMeta); err != nil {
			return nil, fmt.Errorf("failed to encode metadata: %w", err)
		}

		return existingMeta, nil
	}

	// Artifact doesn't exist: create new artifact with data and metadata
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create blob subdirectory: %w", err)
	}

	// 1. Write Artifact Data
	f, err := os.Create(blobPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create artifact file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return nil, fmt.Errorf("failed to write artifact data: %w", err)
	}

	// Get file size for metadata
	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat artifact file: %w", err)
	}
	fileSize := stat.Size()

	// 2. Create metadata
	var finalMeta *models.ArtifactMeta
	if meta != nil {
		// Use provided metadata, but ensure it has the correct hash and length
		finalMeta = &models.ArtifactMeta{
			Hash:             hash,
			Length:           fileSize,
			CreatedTimestamp: meta.CreatedTimestamp,
			References:       meta.References,
		}
		// If no CreatedTimestamp provided, use current time
		if finalMeta.CreatedTimestamp == 0 {
			finalMeta.CreatedTimestamp = stat.ModTime().Unix()
		}
	} else {
		// Create minimal metadata
		finalMeta = &models.ArtifactMeta{
			Hash:             hash,
			Length:           fileSize,
			CreatedTimestamp: stat.ModTime().Unix(),
			References:       []models.ArtifactReference{},
		}
	}

	// 3. Create reference links
	for _, ref := range finalMeta.References {
		if err := s.createOrUpdateRef(&ref, hash); err != nil {
			return nil, fmt.Errorf("failed to create reference link: %w", err)
		}
	}

	// 4. Write Metadata
	metaDir := filepath.Dir(metaPath)
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create meta subdirectory: %w", err)
	}

	metaFile, err := os.Create(metaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create metadata file: %w", err)
	}
	defer metaFile.Close()

	if err := json.NewEncoder(metaFile).Encode(finalMeta); err != nil {
		return nil, fmt.Errorf("failed to encode metadata: %w", err)
	}

	return finalMeta, nil
}

// ReadBlob retrieves the artifact data using standard library SectionReader.
func (s *SimpleFileStorage) ReadBlob(ctx context.Context, req models.ArtifactRange) (io.ReadCloser, models.ArtifactRange, error) {
	// Resolve identifier to hash
	hash, err := s.resolveIdentifier(req.Identifier)
	if err != nil {
		return nil, models.ArtifactRange{}, fmt.Errorf("failed to resolve identifier: %w", err)
	}

	_, blobPath, _ := s.getPaths(hash)

	// Open the file.
	f, err := os.Open(blobPath)
	if err != nil {
		return nil, models.ArtifactRange{}, err
	}

	// Get file size to handle "read until end" (-1)
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, models.ArtifactRange{}, err
	}
	fileSize := stat.Size()

	offset := req.Range.Offset
	if offset < 0 {
		offset = 0
	}

	length := req.Range.Length
	// If length is -1 or extends past EOF, limit it to available bytes
	if length == -1 || offset+length > fileSize {
		length = fileSize - offset
	}
	if length < 0 {
		length = 0
	}

	actualRange := models.ArtifactRange{
		Identifier: models.ArtifactIdentifier{Hash: hash},
		Range: models.ByteRange{
			Offset: offset,
			Length: length,
		},
	}

	// Use standard io.NewSectionReader.
	sectionReader := io.NewSectionReader(f, offset, length)

	// We wrap it to add the Close() method, which must close the underlying file.
	rc := &closingSectionReader{
		SectionReader: sectionReader,
		closer:        f,
	}

	return rc, actualRange, nil
}

// UpdateBlob modifies a range of the artifact.
func (s *SimpleFileStorage) UpdateBlob(ctx context.Context, req models.ArtifactRange, r io.Reader) error {
	// Resolve identifier to hash
	hash, err := s.resolveIdentifier(req.Identifier)
	if err != nil {
		return fmt.Errorf("failed to resolve identifier: %w", err)
	}

	_, blobPath, _ := s.getPaths(hash)

	f, err := os.OpenFile(blobPath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(req.Range.Offset, io.SeekStart); err != nil {
		return err
	}

	if req.Range.Length > 0 {
		_, err = io.CopyN(f, r, req.Range.Length)
	} else {
		_, err = io.Copy(f, r)
	}

	return err
}

// DeleteArtifact removes a specific reference to an artifact.
// If id contains a reference, deletes that reference. If id only contains a hash, deletes all references.
// If no references remain, the artifact is moved to trash and nil is returned.
// If references remain, only the metadata is updated and the updated metadata is returned.
func (s *SimpleFileStorage) DeleteArtifact(ctx context.Context, id models.ArtifactIdentifier) (*models.ArtifactMeta, error) {
	// Resolve identifier to hash
	hash, err := s.resolveIdentifier(id)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve identifier: %w", err)
	}

	// Read existing metadata
	existingMeta, err := s.GetMeta(ctx, id)
	if err != nil {
		if os.IsNotExist(err) {
			// If metadata doesn't exist, check if artifact exists
			_, blobPath, _ := s.getPaths(hash)
			if _, err := os.Stat(blobPath); os.IsNotExist(err) {
				return nil, fmt.Errorf("artifact with hash %s does not exist", hash)
			}
			// Artifact exists but no metadata - this shouldn't happen in normal operation
			// but we'll handle it by just removing the artifact
			if err := s.moveToTrash(ctx, hash); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	// If id has a reference, delete only that reference
	if id.HasReference() {
		ref := id.Reference
		found := false
		newReferences := make([]models.ArtifactReference, 0, len(existingMeta.References))
		for _, existingRef := range existingMeta.References {
			if existingRef.Name == ref.Name && existingRef.Repo == ref.Repo {
				found = true
				// Delete the reference link
				if err := s.deleteRef(&existingRef); err != nil {
					return nil, fmt.Errorf("failed to delete reference link: %w", err)
				}
				// Skip this reference (remove it)
			} else {
				newReferences = append(newReferences, existingRef)
			}
		}

		if !found {
			return nil, fmt.Errorf("reference with name %s and repo %s not found for artifact %s", ref.Name, ref.Repo, hash)
		}

		// Update metadata with remaining references
		existingMeta.References = newReferences
	} else {
		// No reference in id, delete all references
		for _, existingRef := range existingMeta.References {
			if err := s.deleteRef(&existingRef); err != nil {
				return nil, fmt.Errorf("failed to delete reference link: %w", err)
			}
		}
		existingMeta.References = []models.ArtifactReference{}
	}

	// If no references remain, move to trash
	if len(existingMeta.References) == 0 {
		if err := s.moveToTrash(ctx, hash); err != nil {
			return nil, fmt.Errorf("failed to move artifact to trash: %w", err)
		}
		return nil, nil
	}

	// Update metadata file
	_, _, metaPath := s.getPaths(hash)
	metaFile, err := os.Create(metaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to update metadata file: %w", err)
	}
	defer metaFile.Close()

	if err := json.NewEncoder(metaFile).Encode(existingMeta); err != nil {
		return nil, fmt.Errorf("failed to encode updated metadata: %w", err)
	}

	return existingMeta, nil
}

// GetMeta reads the metadata JSON file.
func (s *SimpleFileStorage) GetMeta(ctx context.Context, id models.ArtifactIdentifier) (*models.ArtifactMeta, error) {
	// Resolve identifier to hash
	hash, err := s.resolveIdentifier(id)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve identifier: %w", err)
	}

	_, _, metaPath := s.getPaths(hash)
	f, err := os.Open(metaPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var meta models.ArtifactMeta
	if err := json.NewDecoder(f).Decode(&meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

// UpdateMeta overwrites the metadata JSON file.
func (s *SimpleFileStorage) UpdateMeta(ctx context.Context, meta models.ArtifactMeta) (*models.ArtifactMeta, error) {
	_, _, metaPath := s.getPaths(meta.Hash)
	f, err := os.Create(metaPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// Exists checks if the artifact data and metadata exist using lightweight stat calls.
// It does NOT read the content of the files.
func (s *SimpleFileStorage) Exists(ctx context.Context, hash string) (bool, bool, error) {
	_, blobPath, metaPath := s.getPaths(hash)

	// 1. Check if Artifact binary exists
	blobExists := false
	if _, err := os.Stat(blobPath); err == nil {
		blobExists = true
	} else if !os.IsNotExist(err) {
		// Return actual IO errors (permission, etc.)
		return false, false, err
	}

	// 2. Check if Metadata file exists
	metaExists := false
	if _, err := os.Stat(metaPath); err == nil {
		metaExists = true
	} else if !os.IsNotExist(err) {
		return blobExists, false, err
	}

	return blobExists, metaExists, nil
}

// Move renames an artifact and its metadata to a new hash location.
func (s *SimpleFileStorage) Move(ctx context.Context, srcHash, destHash string) error {
	srcDir, srcBlob, srcMeta := s.getPaths(srcHash)
	destDir, destBlob, destMeta := s.getPaths(destHash)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create dest directory: %w", err)
	}

	// 1. Move Blob
	if err := os.Rename(srcBlob, destBlob); err != nil {
		return fmt.Errorf("failed to move artifact from %s to %s: %w", srcBlob, destBlob, err)
	}

	// 2. Move Metadata (if it exists)
	destMetaDir := filepath.Dir(destMeta)
	if err := os.MkdirAll(destMetaDir, 0755); err != nil {
		return fmt.Errorf("failed to create dest meta directory: %w", err)
	}
	if err := os.Rename(srcMeta, destMeta); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to move metadata: %w", err)
	}

	// 3. Cleanup source directories if they're empty
	if srcDir != destDir {
		_ = os.Remove(srcDir)
	}
	srcMetaDir := filepath.Dir(srcMeta)
	if srcMetaDir != destMetaDir {
		_ = os.Remove(srcMetaDir)
	}

	return nil
}

// --- Helper for Read ---

type closingSectionReader struct {
	*io.SectionReader
	closer io.Closer
}

func (r *closingSectionReader) Close() error {
	return r.closer.Close()
}
