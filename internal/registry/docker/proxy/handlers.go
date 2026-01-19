package proxy

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/collective-projects/brm-server/internal/registry/docker"
)

// SetupRoutes configures HTTP routes for Docker registry API endpoints
func SetupRoutes(mux *http.ServeMux, service *DockerRegistryProxyService) {
	// Catch-all handler for /v2/ endpoints - we'll dispatch based on path and method
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		dispatchRequest(w, r, service)
	})
}

// dispatchRequest routes requests to appropriate handlers based on path and method
func dispatchRequest(w http.ResponseWriter, r *http.Request, service *DockerRegistryProxyService) {
	path := r.URL.Path
	method := r.Method

	// API version check: GET /v2/
	if path == "/v2/" || path == "/v2" {
		if method == http.MethodGet {
			handleAPIVersion(w, r)
			return
		}
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
			default:
				docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
			}
			return
		}
	}

	// Match blob endpoints: /v2/{name}/blobs/{digest}
	if strings.Contains(pathRest, "/blobs/") {
		parts := strings.Split(pathRest, "/blobs/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			switch method {
			case http.MethodGet:
				handleGetBlob(w, r, service)
			case http.MethodHead:
				handleHeadBlob(w, r, service)
			default:
				docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
			}
			return
		}
	}

	// No route matched
	http.NotFound(w, r)
}

// handleAPIVersion handles GET /v2/ - API version check
func handleAPIVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

// handleGetManifest handles GET /v2/{name}/manifests/{reference}
func handleGetManifest(w http.ResponseWriter, r *http.Request, service *DockerRegistryProxyService) {
	if r.Method != http.MethodGet {
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, reference, err := parseManifestPath(r.URL.Path)
	if err != nil {
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
func handleHeadManifest(w http.ResponseWriter, r *http.Request, service *DockerRegistryProxyService) {
	if r.Method != http.MethodHead {
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, reference, err := parseManifestPath(r.URL.Path)
	if err != nil {
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

// handleGetBlob handles GET /v2/{name}/blobs/{digest}
func handleGetBlob(w http.ResponseWriter, r *http.Request, service *DockerRegistryProxyService) {
	if r.Method != http.MethodGet {
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, digest, err := parseBlobPath(r.URL.Path)
	if err != nil {
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
func handleHeadBlob(w http.ResponseWriter, r *http.Request, service *DockerRegistryProxyService) {
	if r.Method != http.MethodHead {
		docker.WriteError(w, docker.ErrUnsupported("method not allowed"))
		return
	}

	name, digest, err := parseBlobPath(r.URL.Path)
	if err != nil {
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
