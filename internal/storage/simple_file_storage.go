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
		BaseStorage: models.BaseStorage{
			Alias: alias,
		},
	}
	return s, nil
}

// computeReferenceHash computes SHA256 of "Registry::Name" for reference-based storage.
func computeReferenceHash(ref *models.ArtifactReference) string {
	input := ref.Registry + "::" + ref.Name
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

// getPaths returns the directory, artifact/blob path, and metadata path for a given hash.
// Uses blob/ and meta/ subdirectories with 2-character git-like structure.
func (s *SimpleFileStorage) getPaths(hash string) (dir, blobPath, metaPath string) {
	if len(hash) < 2 {
		dir = filepath.Join(s.baseDir, "blob")
		blobPath = filepath.Join(dir, hash)
		metaPath = filepath.Join(s.baseDir, "meta", hash)
	} else {
		subDir := hash[:2]
		fileName := hash[2:]
		dir = filepath.Join(s.baseDir, "blob", subDir)
		blobPath = filepath.Join(dir, fileName)
		metaPath = filepath.Join(s.baseDir, "meta", subDir, fileName)
	}
	return
}

// getRefPath returns the reference file path for a given reference hash.
func (s *SimpleFileStorage) getRefPath(refHash string) (dir, refPath string) {
	if len(refHash) < 2 {
		dir = filepath.Join(s.baseDir, "ref")
		refPath = filepath.Join(dir, refHash)
	} else {
		subDir := refHash[:2]
		fileName := refHash[2:]
		dir = filepath.Join(s.baseDir, "ref", subDir)
		refPath = filepath.Join(dir, fileName)
	}
	return
}

// getMetaRefPaths returns the directory and file path for a reference within an artifact's metaref folder.
// Structure: metaref/{hash[:2]}/{hash[2:]}/{refHash}
func (s *SimpleFileStorage) getMetaRefPaths(hash, refHash string) (dir, refFilePath string) {
	if len(hash) < 2 {
		dir = filepath.Join(s.baseDir, "metaref", hash)
		refFilePath = filepath.Join(dir, refHash)
	} else {
		subDir := hash[:2]
		fileName := hash[2:]
		dir = filepath.Join(s.baseDir, "metaref", subDir, fileName)
		refFilePath = filepath.Join(dir, refHash)
	}
	return
}

// getTrashPath returns the trash directory path for a given hash using git-like structure.
func (s *SimpleFileStorage) getTrashPath(hash string) (dir, blobPath, metaPath string) {
	if len(hash) < 2 {
		dir = filepath.Join(s.baseDir, ".trash", "blob")
		blobPath = filepath.Join(dir, hash)
		metaPath = filepath.Join(s.baseDir, ".trash", "meta", hash)
	} else {
		subDir := hash[:2]
		fileName := hash[2:]
		dir = filepath.Join(s.baseDir, ".trash", "blob", subDir)
		blobPath = filepath.Join(dir, fileName)
		metaPath = filepath.Join(s.baseDir, ".trash", "meta", subDir, fileName)
	}
	return
}

// ResolveIdentifier resolves an ArtifactIdentifier to its canonical storage hash.
// Exposed (in addition to the internal resolveIdentifier) so wrapper storages, such as
// ConcurrentArtifactStorage, can determine the correct lock key for reference-only identifiers.
func (s *SimpleFileStorage) ResolveIdentifier(ctx context.Context, id models.ArtifactIdentifier) (string, error) {
	return s.resolveIdentifier(id)
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

// createOrUpdateMetaRefFile creates or updates a reference file in the metaref folder.
// Validates tag constraints: keys ≤128 chars, values ≤1024 chars.
func (s *SimpleFileStorage) createOrUpdateMetaRefFile(hash string, ref *models.ArtifactReference) error {
	// Validate tags
	for key, value := range ref.Tags {
		if len(key) > 128 {
			return fmt.Errorf("tag key exceeds 128 characters: %s", key)
		}
		if len(value) > 1024 {
			return fmt.Errorf("tag value exceeds 1024 characters for key %s", key)
		}
	}

	refHash := computeReferenceHash(ref)
	refDir, refFilePath := s.getMetaRefPaths(hash, refHash)

	if err := os.MkdirAll(refDir, 0755); err != nil {
		return fmt.Errorf("failed to create metaref directory: %w", err)
	}

	f, err := os.Create(refFilePath)
	if err != nil {
		return fmt.Errorf("failed to create metaref file: %w", err)
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(ref); err != nil {
		return fmt.Errorf("failed to encode reference: %w", err)
	}

	return nil
}

// deleteMetaRefFile removes a reference file from the metaref folder and cleans up empty directories.
func (s *SimpleFileStorage) deleteMetaRefFile(hash string, ref *models.ArtifactReference) error {
	refHash := computeReferenceHash(ref)
	refDir, refFilePath := s.getMetaRefPaths(hash, refHash)

	err := os.Remove(refFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove metaref file: %w", err)
	}

	// Try to clean up empty directory
	_ = os.Remove(refDir)

	return nil
}

// listMetaRefFiles returns all reference hashes for an artifact.
func (s *SimpleFileStorage) listMetaRefFiles(hash string) ([]string, error) {
	refDir, _ := s.getMetaRefPaths(hash, "")
	// refDir already points to the directory containing all ref files for this hash

	entries, err := os.ReadDir(refDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil // No references
		}
		return nil, fmt.Errorf("failed to read metaref directory: %w", err)
	}

	var refHashes []string
	for _, entry := range entries {
		if !entry.IsDir() {
			refHashes = append(refHashes, entry.Name())
		}
	}

	return refHashes, nil
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

	// Move metaref folder (if it exists)
	_, srcMetaRefDir := s.getMetaRefPaths(hash, "")
	srcMetaRefDir = filepath.Dir(srcMetaRefDir) // Get the directory containing all ref files
	if _, err := os.Stat(srcMetaRefDir); err == nil {
		// Metaref directory exists, move it
		var destMetaRefDir string
		if len(hash) < 2 {
			destMetaRefDir = filepath.Join(s.baseDir, ".trash", "metaref", hash)
		} else {
			subDir := hash[:2]
			fileName := hash[2:]
			destMetaRefDir = filepath.Join(s.baseDir, ".trash", "metaref", subDir, fileName)
		}
		parentDir := filepath.Dir(destMetaRefDir)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return fmt.Errorf("failed to create trash metaref directory: %w", err)
		}
		if err := os.Rename(srcMetaRefDir, destMetaRefDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to move metaref to trash: %w", err)
		}
	}

	return nil
}

// CreateArtifact stores the artifact and optional metadata.
// If artifact already exists, validates length and adds new references atomically.
// References are stored in metaref/ folder, not in metadata.
func (s *SimpleFileStorage) CreateArtifact(ctx context.Context, id models.ArtifactIdentifier, r io.Reader, size int64, meta *models.ArtifactMeta) (*models.ArtifactMeta, error) {
	// Resolve identifier to hash
	var hash string
	var newReferences []models.ArtifactReference

	if id.HasHash() {
		hash = id.Hash
		// If a reference is also provided, include it in new references
		if id.HasReference() {
			newReferences = append(newReferences, *id.Reference)
		}
	} else if id.HasReference() {
		// For new artifacts with reference, we need the hash from metadata
		if meta == nil || meta.Hash == "" {
			return nil, fmt.Errorf("creating artifact by reference requires metadata with hash")
		}
		hash = meta.Hash
		// Add the reference from identifier to new references
		newReferences = append(newReferences, *id.Reference)
	} else {
		return nil, fmt.Errorf("invalid identifier")
	}

	// Check if both id.hash and meta.hash are present, they match
	if id.HasHash() && meta != nil && meta.Hash != "" && id.Hash != meta.Hash {
		return nil, fmt.Errorf("hash mismatch between identifier and metadata")
	}

	// Get paths
	dir, blobPath, metaPath := s.getPaths(hash)

	// Check if artifact file already exists
	_, err := os.Stat(blobPath)
	artifactExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to check artifact existence: %w", err)
	}

	if artifactExists {
		// Artifact exists: read existing metadata, validate length, add new references
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

		// Add new references to metaref/ folder
		// Atomically create/update reference files for each new reference
		for _, ref := range newReferences {
			// Create ref lookup file
			if err := s.createOrUpdateRef(&ref, hash); err != nil {
				return nil, fmt.Errorf("failed to create reference link: %w", err)
			}
			// Create metaref file with full reference data
			if err := s.createOrUpdateMetaRefFile(hash, &ref); err != nil {
				return nil, fmt.Errorf("failed to create metaref file: %w", err)
			}
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

	// 2. Create metadata (without references)
	var finalMeta *models.ArtifactMeta
	if meta != nil {
		// Use provided metadata, but ensure it has the correct hash and length
		finalMeta = &models.ArtifactMeta{
			Hash:             hash,
			Length:           fileSize,
			CreatedTimestamp: meta.CreatedTimestamp,
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
		}
	}

	// 3. Write Metadata (before references for atomicity)
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

	// 4. Create reference links and metaref files atomically
	for _, ref := range newReferences {
		// Create ref lookup file
		if err := s.createOrUpdateRef(&ref, hash); err != nil {
			return nil, fmt.Errorf("failed to create reference link: %w", err)
		}
		// Create metaref file with full reference data
		if err := s.createOrUpdateMetaRefFile(hash, &ref); err != nil {
			return nil, fmt.Errorf("failed to create metaref file: %w", err)
		}
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
// If no references remain, the artifact is moved to trash.
func (s *SimpleFileStorage) DeleteArtifact(ctx context.Context, id models.ArtifactIdentifier) error {
	// Resolve identifier to hash
	hash, err := s.resolveIdentifier(id)
	if err != nil {
		return fmt.Errorf("failed to resolve identifier: %w", err)
	}

	// Check if artifact exists
	_, blobPath, _ := s.getPaths(hash)
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		return fmt.Errorf("artifact with hash %s does not exist", hash)
	}

	// If id has a reference, delete only that reference
	if id.HasReference() {
		ref := id.Reference

		// Verify reference exists before deleting
		if _, err := s.GetReference(ctx, hash, ref.Name, ref.Registry); err != nil {
			return fmt.Errorf("reference with name %s and registry %s not found for artifact %s", ref.Name, ref.Registry, hash)
		}

		// Delete the reference link (ref/ folder)
		if err := s.deleteRef(ref); err != nil {
			return fmt.Errorf("failed to delete reference link: %w", err)
		}

		// Delete the metaref file
		if err := s.deleteMetaRefFile(hash, ref); err != nil {
			return fmt.Errorf("failed to delete metaref file: %w", err)
		}
	} else {
		// No reference in id, delete all references
		refHashes, err := s.listMetaRefFiles(hash)
		if err != nil {
			return fmt.Errorf("failed to list references: %w", err)
		}

		// Delete all reference files and links
		for _, refHash := range refHashes {
			// Read the reference to get name and registry for deleting ref link
			_, refFilePath := s.getMetaRefPaths(hash, refHash)
			f, err := os.Open(refFilePath)
			if err != nil {
				continue // Skip if file doesn't exist
			}
			var ref models.ArtifactReference
			if err := json.NewDecoder(f).Decode(&ref); err != nil {
				f.Close()
				continue // Skip malformed references
			}
			f.Close()

			// Delete ref link and metaref file
			_ = s.deleteRef(&ref)
			_ = s.deleteMetaRefFile(hash, &ref)
		}
	}

	// Check if any references remain
	refHashes, err := s.listMetaRefFiles(hash)
	if err != nil {
		return fmt.Errorf("failed to check remaining references: %w", err)
	}

	// If no references remain, move to trash
	if len(refHashes) == 0 {
		if err := s.moveToTrash(ctx, hash); err != nil {
			return fmt.Errorf("failed to move artifact to trash: %w", err)
		}
	}

	return nil
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

// GetReference retrieves a specific reference by name and registry for an artifact.
func (s *SimpleFileStorage) GetReference(ctx context.Context, hash, name, registry string) (*models.ArtifactReference, error) {
	ref := &models.ArtifactReference{
		Name:     name,
		Registry: registry,
	}
	refHash := computeReferenceHash(ref)
	_, refFilePath := s.getMetaRefPaths(hash, refHash)

	f, err := os.Open(refFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("reference %s::%s not found for artifact %s", registry, name, hash)
		}
		return nil, fmt.Errorf("failed to open reference file: %w", err)
	}
	defer f.Close()

	var loadedRef models.ArtifactReference
	if err := json.NewDecoder(f).Decode(&loadedRef); err != nil {
		return nil, fmt.Errorf("failed to decode reference: %w", err)
	}

	return &loadedRef, nil
}

// UpdateReference updates an existing reference. Only Description and Tags can be modified.
// Name and Registry fields are immutable and used for identification.
// Tag keys must be ≤128 chars, values ≤1024 chars.
func (s *SimpleFileStorage) UpdateReference(ctx context.Context, hash string, ref models.ArtifactReference) (*models.ArtifactReference, error) {
	// Validate that the reference exists first
	existingRef, err := s.GetReference(ctx, hash, ref.Name, ref.Registry)
	if err != nil {
		return nil, err
	}

	// Update only mutable fields
	existingRef.Description = ref.Description
	existingRef.Tags = ref.Tags
	// Keep ReferencedTimestamp from existing reference (immutable)

	// Validate and save
	if err := s.createOrUpdateMetaRefFile(hash, existingRef); err != nil {
		return nil, err
	}

	return existingRef, nil
}

// ListReferenceHashes returns all reference hashes (SHA256 of Registry::Name) for an artifact.
func (s *SimpleFileStorage) ListReferenceHashes(ctx context.Context, hash string) ([]string, error) {
	return s.listMetaRefFiles(hash)
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
