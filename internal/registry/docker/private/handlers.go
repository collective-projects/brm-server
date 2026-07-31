package private

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/collective-projects/brm-server/internal/registry/docker"
)

// SetupRoutes configures HTTP routes for Docker registry API endpoints
func SetupRoutes(mux *http.ServeMux, service *DockerRegistryPrivateService) {
	// Catch-all handler for /v2/ endpoints - we'll dispatch based on path and method
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		dispatchRequest(w, r, service)
	})
	mux.HandleFunc("/v2", func(w http.ResponseWriter, r *http.Request) {
		dispatchRequest(w, r, service)
	})
}

// dispatchRequest routes requests to appropriate handlers based on path and method
func dispatchRequest(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	path := r.URL.Path
	method := r.Method

	slog.Debug("Request received", "method", method, "path", path)

	// API version check: GET /v2/
	if path == "/v2/" || path == "/v2" {
		if method == http.MethodGet {
			handleAPIVersion(w, r)
			return
		}
		slog.Warn("Method not allowed for API version check", "method", method, "path", path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	// Remove /v2/ prefix for easier parsing
	pathRest := strings.TrimPrefix(path, "/v2/")

	// Match manifest endpoints: /v2/{name}/manifests/{reference}
	if strings.Contains(pathRest, "/manifests/") {
		parts := strings.Split(pathRest, "/manifests/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			switch method {
			case http.MethodGet:
				handleGetManifest(w, r, service)
			case http.MethodHead:
				handleHeadManifest(w, r, service)
			case http.MethodPut:
				handlePutManifest(w, r, service)
			default:
				slog.Warn("Method not allowed for manifest endpoint", "method", method, "path", path)
				docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
			}
			return
		}
	}

	// Match blob endpoints: /v2/{name}/blobs/{digest}
	if strings.Contains(pathRest, "/blobs/") && !strings.Contains(pathRest, "/blobs/uploads") {
		parts := strings.Split(pathRest, "/blobs/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			switch method {
			case http.MethodGet:
				handleGetBlob(w, r, service)
			case http.MethodHead:
				handleHeadBlob(w, r, service)
			default:
				slog.Warn("Method not allowed for blob endpoint", "method", method, "path", path)
				docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
			}
			return
		}
	}

	// Match blob upload endpoints: /v2/{name}/blobs/uploads/
	if strings.Contains(pathRest, "/blobs/uploads/") {
		parts := strings.Split(pathRest, "/blobs/uploads/")
		if len(parts) == 2 && parts[0] != "" {
			uuid := parts[1]
			// POST /v2/{name}/blobs/uploads/ (start upload)
			if uuid == "" {
				if method == http.MethodPost {
					handleStartBlobUpload(w, r, service)
					return
				}
				slog.Warn("Method not allowed for start blob upload", "method", method, "path", path)
				docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
				return
			}
			// PATCH or PUT /v2/{name}/blobs/uploads/{uuid}
			switch method {
			case http.MethodPatch:
				handleUploadBlobChunk(w, r, service)
			case http.MethodPut:
				handleCompleteBlobUpload(w, r, service)
			default:
				slog.Warn("Method not allowed for blob upload session", "method", method, "path", path)
				docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
			}
			return
		}
	}

	// No route matched
	slog.Warn("No matching route", "method", method, "path", path)
	http.NotFound(w, r)
}

// handleAPIVersion handles GET /v2/ - API version check
func handleAPIVersion(w http.ResponseWriter, r *http.Request) {
	slog.Debug("API version check", "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodGet {
		slog.Warn("Method not allowed for API version check", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
	slog.Debug("API version check successful")
}

// handleGetManifest handles GET /v2/{name}/manifests/{reference}
func handleGetManifest(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("GET manifest request", "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodGet {
		slog.Warn("Method not allowed for GET manifest", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, reference, err := parseManifestPath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse manifest path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrNameUnknown(""))
		return
	}

	manifestData, mediaType, err := service.GetManifest(r.Context(), name, reference)
	if err != nil {
		docker.WriteError(w, docker.ErrManifestUnknown(reference))
		return
	}

	// Set headers per OCI Distribution Spec
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(manifestData)))

	// Calculate and set Docker-Content-Digest
	digest := service.CalculateDigest(manifestData)
	w.Header().Set("Docker-Content-Digest", digest)

	w.WriteHeader(http.StatusOK)
	w.Write(manifestData)
}

// handleHeadManifest handles HEAD /v2/{name}/manifests/{reference}
func handleHeadManifest(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("HEAD manifest request", "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodHead {
		slog.Warn("Method not allowed for HEAD manifest", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, reference, err := parseManifestPath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse manifest path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrNameUnknown(""))
		return
	}

	exists, digest, err := service.CheckManifestExists(r.Context(), name, reference)
	if err != nil {
		docker.WriteError(w, docker.ErrManifestUnknown(reference))
		return
	}

	if !exists {
		docker.WriteError(w, docker.ErrManifestUnknown(reference))
		return
	}

	// Set headers
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
}

// handlePutManifest handles PUT /v2/{name}/manifests/{reference}
func handlePutManifest(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("PUT manifest request", "method", r.Method, "path", r.URL.Path, "contentType", r.Header.Get("Content-Type"))

	if r.Method != http.MethodPut {
		slog.Warn("Method not allowed for PUT manifest", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, reference, err := parseManifestPath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse manifest path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrNameUnknown(""))
		return
	}

	// Read manifest data
	manifestData, err := io.ReadAll(r.Body)
	if err != nil {
		docker.WriteError(w, docker.ErrManifestInvalid("failed to read manifest data"))
		return
	}
	defer r.Body.Close()

	// Get media type from Content-Type header
	mediaType := r.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = docker.MediaTypeOCIManifest // Default
	}

	// Store manifest
	err = service.PutManifest(r.Context(), name, reference, manifestData, mediaType)
	if err != nil {
		docker.WriteError(w, docker.ErrManifestInvalid(err.Error()))
		return
	}

	// Calculate and set Docker-Content-Digest
	digest := service.CalculateDigest(manifestData)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, reference))
	w.WriteHeader(http.StatusCreated)
}

// handleGetBlob handles GET /v2/{name}/blobs/{digest}
func handleGetBlob(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("GET blob request", "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodGet {
		slog.Warn("Method not allowed for GET blob", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, digest, err := parseBlobPath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse blob path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrNameUnknown(""))
		return
	}

	blobReader, size, err := service.GetBlob(r.Context(), name, digest)
	if err != nil {
		docker.WriteError(w, docker.ErrBlobUnknown(digest))
		return
	}
	defer blobReader.Close()

	// Set headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Docker-Content-Digest", digest)

	w.WriteHeader(http.StatusOK)
	io.Copy(w, blobReader)
}

// handleHeadBlob handles HEAD /v2/{name}/blobs/{digest}
func handleHeadBlob(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("HEAD blob request (checking if blob exists)", "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodHead {
		slog.Warn("Method not allowed for HEAD blob", "method", r.Method, "path", r.URL.Path, "expectedMethod", "HEAD")
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, digest, err := parseBlobPath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse blob path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrNameUnknown(""))
		return
	}

	exists, size, err := service.CheckBlobExists(r.Context(), name, digest)
	if err != nil {
		docker.WriteError(w, docker.ErrBlobUnknown(digest))
		return
	}

	if !exists {
		docker.WriteError(w, docker.ErrBlobUnknown(digest))
		return
	}

	// Set headers
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
}

// handleStartBlobUpload handles POST /v2/{name}/blobs/uploads/
func handleStartBlobUpload(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("POST start blob upload request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)

	if r.Method != http.MethodPost {
		slog.Warn("Method not allowed for POST blob upload", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, err := parseBlobUploadBasePath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse blob upload path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrNameUnknown(""))
		return
	}

	// Check for single-request upload (digest in query)
	digest := r.URL.Query().Get("digest")
	if digest != "" {
		// Single-request upload
		handleSingleRequestBlobUpload(w, r, service, name, digest)
		return
	}

	// Create upload session
	uuid, err := service.StartBlobUpload(r.Context(), name)
	if err != nil {
		docker.WriteError(w, docker.ErrBlobUploadUnknown("failed to create upload session"))
		return
	}

	// Return session UUID in Location header
	location := fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid)
	w.Header().Set("Location", location)
	w.Header().Set("Range", "0-0")
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.WriteHeader(http.StatusAccepted)
}

// handleSingleRequestBlobUpload handles POST /v2/{name}/blobs/uploads/?digest={digest}
func handleSingleRequestBlobUpload(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService, name, digest string) {
	// r.ContentLength is -1 when the client didn't send Content-Length; PutBlob/storage
	// already handle an unknown size, so the body is streamed through as-is either way.
	err := service.PutBlob(r.Context(), name, digest, r.Body, r.ContentLength)
	if err != nil {
		if strings.Contains(err.Error(), "digest mismatch") {
			docker.WriteError(w, docker.ErrBlobUploadInvalid("digest mismatch"))
		} else {
			docker.WriteError(w, docker.ErrBlobUploadUnknown(err.Error()))
		}
		return
	}

	// Set headers
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.WriteHeader(http.StatusCreated)
}

// handleUploadBlobChunk handles PATCH /v2/{name}/blobs/uploads/{uuid}
func handleUploadBlobChunk(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("PATCH upload blob chunk request", "method", r.Method, "path", r.URL.Path)

	if r.Method != http.MethodPatch {
		slog.Warn("Method not allowed for PATCH blob upload", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, uuid, err := parseBlobUploadPath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse blob upload path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrBlobUploadUnknown("invalid upload path"))
		return
	}

	// Parse Range header for offset
	offset := int64(0)
	rangeHeader := r.Header.Get("Content-Range")
	if rangeHeader != "" {
		// Parse "bytes start-end/total" or "bytes start-end/*"
		parts := strings.Split(rangeHeader, " ")
		if len(parts) == 2 && parts[0] == "bytes" {
			rangePart := parts[1]
			if strings.Contains(rangePart, "-") {
				rangeParts := strings.Split(rangePart, "-")
				if len(rangeParts) == 2 {
					if parsedOffset, err := strconv.ParseInt(rangeParts[0], 10, 64); err == nil {
						offset = parsedOffset
					}
				}
			}
		}
	}

	// Upload chunk
	newOffset, err := service.UploadBlobChunk(r.Context(), name, uuid, r.Body, offset)
	if err != nil {
		slog.Warn("Failed to upload blob chunk", "name", name, "uuid", uuid, "error", err)
		docker.WriteError(w, docker.ErrBlobUploadUnknown(err.Error()))
		return
	}

	// Set headers per Docker Registry HTTP API V2 spec
	location := fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid)
	w.Header().Set("Location", location)
	w.Header().Set("Range", fmt.Sprintf("0-%d", newOffset-1))
	w.Header().Set("Docker-Upload-UUID", uuid)
	w.WriteHeader(http.StatusAccepted)
	slog.Debug("Blob chunk uploaded successfully", "name", name, "uuid", uuid, "newOffset", newOffset)
}

// handleCompleteBlobUpload handles PUT /v2/{name}/blobs/uploads/{uuid}?digest={digest}
func handleCompleteBlobUpload(w http.ResponseWriter, r *http.Request, service *DockerRegistryPrivateService) {
	slog.Debug("PUT complete blob upload request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)

	if r.Method != http.MethodPut {
		slog.Warn("Method not allowed for PUT complete blob upload", "method", r.Method, "path", r.URL.Path)
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, uuid, err := parseBlobUploadPath(r.URL.Path)
	if err != nil {
		slog.Warn("Failed to parse blob upload path", "path", r.URL.Path, "error", err)
		docker.WriteError(w, docker.ErrBlobUploadUnknown("invalid upload path"))
		return
	}

	// Get digest from query parameter
	digest := r.URL.Query().Get("digest")
	if digest == "" {
		docker.WriteError(w, docker.ErrBlobUploadInvalid("digest parameter required"))
		return
	}

	// Complete upload (final chunk is in request body)
	err = service.CompleteBlobUpload(r.Context(), name, uuid, digest, r.Body)
	if err != nil {
		if strings.Contains(err.Error(), "digest mismatch") {
			docker.WriteError(w, docker.ErrBlobUploadInvalid("digest mismatch"))
		} else {
			docker.WriteError(w, docker.ErrBlobUploadUnknown(err.Error()))
		}
		return
	}

	// Set headers
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, digest))
	w.WriteHeader(http.StatusCreated)
}

// parseManifestPath extracts name and reference from /v2/{name}/manifests/{reference}
func parseManifestPath(path string) (string, string, error) {
	// Remove /v2/ prefix
	if !strings.HasPrefix(path, "/v2/") {
		return "", "", fmt.Errorf("invalid path format")
	}
	path = path[4:]

	// Find /manifests/
	parts := strings.Split(path, "/manifests/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid manifest path format")
	}

	name := parts[0]
	reference := parts[1]

	if name == "" || reference == "" {
		return "", "", fmt.Errorf("name and reference cannot be empty")
	}

	return name, reference, nil
}

// parseBlobPath extracts name and digest from /v2/{name}/blobs/{digest}
func parseBlobPath(path string) (string, string, error) {
	// Remove /v2/ prefix
	if !strings.HasPrefix(path, "/v2/") {
		return "", "", fmt.Errorf("invalid path format")
	}
	path = path[4:]

	// Find /blobs/
	parts := strings.Split(path, "/blobs/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid blob path format")
	}

	name := parts[0]
	digest := parts[1]

	if name == "" || digest == "" {
		return "", "", fmt.Errorf("name and digest cannot be empty")
	}

	return name, digest, nil
}

// parseBlobUploadBasePath extracts name from /v2/{name}/blobs/uploads/
func parseBlobUploadBasePath(path string) (string, error) {
	// Remove /v2/ prefix
	if !strings.HasPrefix(path, "/v2/") {
		return "", fmt.Errorf("invalid path format")
	}
	path = path[4:]

	// Find /blobs/uploads/
	parts := strings.Split(path, "/blobs/uploads/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid blob upload path format")
	}

	name := parts[0]
	if name == "" {
		return "", fmt.Errorf("name cannot be empty")
	}

	return name, nil
}

// parseBlobUploadPath extracts name and uuid from /v2/{name}/blobs/uploads/{uuid}
func parseBlobUploadPath(path string) (string, string, error) {
	// Remove /v2/ prefix
	if !strings.HasPrefix(path, "/v2/") {
		return "", "", fmt.Errorf("invalid path format")
	}
	path = path[4:]

	// Find /blobs/uploads/
	parts := strings.Split(path, "/blobs/uploads/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid blob upload path format")
	}

	name := parts[0]
	uuid := parts[1]

	// Remove query string from uuid if present
	if idx := strings.Index(uuid, "?"); idx >= 0 {
		uuid = uuid[:idx]
	}

	if name == "" || uuid == "" {
		return "", "", fmt.Errorf("name and uuid cannot be empty")
	}

	return name, uuid, nil
}
