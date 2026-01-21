package models

import (
	"context"
	"fmt"
	"io"
)

// ArtifactReference represents a reference to an artifact by a specific name and repo
type ArtifactReference struct {
	Name                string `json:"name"`
	Repo                string `json:"repo"`
	ReferencedTimestamp int64  `json:"referencedTimestamp"`
}

// ArtifactMeta holds metadata about an artifact
type ArtifactMeta struct {
	Hash             string              `json:"hash"`
	Length           int64               `json:"length"`
	CreatedTimestamp int64               `json:"createdTimestamp"` // When artifact data was first created
	References       []ArtifactReference `json:"references"`       // List of references to this artifact
}

// HashConflictError is returned when Create is called with a size that doesn't match an existing artifact
type HashConflictError struct {
	Hash           string
	ExistingLength int64
	ProvidedLength int64
	Message        string
}

func (e *HashConflictError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("hash conflict: artifact with hash %s already exists with length %d, but provided length is %d", e.Hash, e.ExistingLength, e.ProvidedLength)
}

// Artifact struct is REMOVED.
// We do not want a struct representing the binary data in memory.
// We use io.Reader and io.ReadCloser instead.

// ByteRange represents a byte range for partial content requests.
// Switching to Length is preferred over End for consistency with Go IO interfaces.
type ByteRange struct {
	Offset int64 `json:"offset"` // Starting byte position
	Length int64 `json:"length"` // Number of bytes to read/write. -1 means "until the end"
}

// ArtifactRange identifies a specific range within an artifact by hash.
// It replaces ArtifactRequest, ArtifactResponse, and ArtifactRangeUpdate.
type ArtifactRange struct {
	Hash  string    `json:"hash"`
	Range ByteRange `json:"range"`
}

// ArtifactStorageInfo contains information about a storage instance.
type ArtifactStorageInfo struct {
	Alias string `json:"alias"` // The alias/name of the storage
}

// BaseStorage provides common functionality for all storage implementations.
type BaseStorage struct {
	alias string
}

// GetStorageInfo returns information about the storage instance.
func (b *BaseStorage) GetStorageInfo() ArtifactStorageInfo {
	return ArtifactStorageInfo{
		Alias: b.alias,
	}
}

// SetAlias sets the alias/name of the storage.
func (b *BaseStorage) SetAlias(alias string) {
	b.alias = alias
}

// ArtifactStorage is the high-performance interface.
type ArtifactStorage interface {
	// CreateArtifact streams data from 'r' to storage.
	// We include 'size' because many storage backends (allocators/S3) need a size hint.
	// If size is unknown, pass -1 (though this may disable some optimizations).
	// The 'meta' parameter is optional (can be nil). If provided, metadata is stored atomically with the data.
	// Returns the final metadata state (merged if artifact existed, new if created).
	// If artifact exists, validates length match and merges references without writing data.
	// Implementations are definitely expected to support this method.
	// Implementations should handle their thread-safety internally, if they are declared as thread-safe.
	CreateArtifact(ctx context.Context, hash string, r io.Reader, size int64, meta *ArtifactMeta) (*ArtifactMeta, error)

	// ReadBlob returns a stream (rc) for the requested data.
	// It returns 'actual' containing the actual range being returned (calculated).
	// This is useful if the requested Length was -1 or exceeded the file size.
	// IMPORTANT: The caller MUST close rc.
	// Implementations are definitely expected to suport this method.
	ReadBlob(ctx context.Context, req ArtifactRange) (rc io.ReadCloser, actual ArtifactRange, err error)

	// UpdateBlob modifies a specific range by streaming data from 'r'.
	// Implementations may omit this method, if they are read-only.
	UpdateBlob(ctx context.Context, req ArtifactRange, r io.Reader) error

	// DeleteArtifact removes a specific reference to an artifact.
	// If no references remain, the artifact is moved to trash and nil is returned.
	// If references remain, only the metadata is updated and the updated metadata is returned.
	// Implementations are definitely expected to suport this method.
	// Implementations should handle their thread-safety internally, if they are declared as thread-safe. Must be blocked if a Create operation is in progress.
	DeleteArtifact(ctx context.Context, hash string, ref ArtifactReference) (*ArtifactMeta, error)

	// Meta operations
	// Implementations are definitely expected to suport this method.
	GetMeta(ctx context.Context, hash string) (*ArtifactMeta, error)

	// UpdateMeta updates the metadata for an artifact. Clients may need to store additional small info in metadata.
	// Implementations are definitely expected to suport this method.
	// Implementations should handle their thread-safety internally, if they are declared as thread-safe.
	UpdateMeta(ctx context.Context, meta ArtifactMeta) (*ArtifactMeta, error)

	// GetStorageInfo returns information about the storage instance.
	GetStorageInfo() ArtifactStorageInfo
}
