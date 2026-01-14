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
	// API version check
	mux.HandleFunc("GET /v2/", func(w http.ResponseWriter, r *http.Request) {
		handleAPIVersion(w, r)
	})

	// Manifest endpoints
	mux.HandleFunc("GET /v2/{name}/manifests/{reference}", func(w http.ResponseWriter, r *http.Request) {
		handleGetManifest(w, r, service)
	})
	mux.HandleFunc("HEAD /v2/{name}/manifests/{reference}", func(w http.ResponseWriter, r *http.Request) {
		handleHeadManifest(w, r, service)
	})

	// Blob endpoints
	mux.HandleFunc("GET /v2/{name}/blobs/{digest}", func(w http.ResponseWriter, r *http.Request) {
		handleGetBlob(w, r, service)
	})
	mux.HandleFunc("HEAD /v2/{name}/blobs/{digest}", func(w http.ResponseWriter, r *http.Request) {
		handleHeadBlob(w, r, service)
	})
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
