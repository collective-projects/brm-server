package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/collective-projects/brm-server/pkg/models"
)

// DockerRegistryProxyClient handles HTTP communication with upstream Docker registries
type DockerRegistryProxyClient struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// NewDockerRegistryProxyClient creates a new client for upstream registry communication
func NewDockerRegistryProxyClient(upstream *models.UpstreamRegistry) *DockerRegistryProxyClient {
	return &DockerRegistryProxyClient{
		baseURL:  upstream.URL,
		username: upstream.Username,
		password: upstream.Password,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// makeRequest makes an HTTP request to the upstream registry with authentication
func (c *DockerRegistryProxyClient) makeRequest(ctx context.Context, method, path string, headers map[string]string) (*http.Response, error) {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication if credentials are provided
	if c.username != "" && c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	// Add custom headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Set Accept header for manifests
	if method == http.MethodGet && (path[len(path)-9:] == "/manifests" || path[len(path)-10:] == "/manifest/") {
		req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, */*")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	return resp, nil
}

// GetManifest fetches a manifest from the upstream registry
func (c *DockerRegistryProxyClient) GetManifest(ctx context.Context, name, reference string) ([]byte, string, error) {
	path := fmt.Sprintf("/v2/%s/manifests/%s", name, reference)
	resp, err := c.makeRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream registry returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response body: %w", err)
	}

	mediaType := resp.Header.Get("Content-Type")

	return body, mediaType, nil
}

// CheckManifestExists checks if a manifest exists in the upstream registry
func (c *DockerRegistryProxyClient) CheckManifestExists(ctx context.Context, name, reference string) (bool, string, error) {
	path := fmt.Sprintf("/v2/%s/manifests/%s", name, reference)
	resp, err := c.makeRequest(ctx, http.MethodHead, path, nil)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		digest := resp.Header.Get("Docker-Content-Digest")
		return true, digest, nil
	}

	return false, "", nil
}

// GetBlob fetches a blob from the upstream registry
func (c *DockerRegistryProxyClient) GetBlob(ctx context.Context, name, digest string) (io.ReadCloser, int64, error) {
	path := fmt.Sprintf("/v2/%s/blobs/%s", name, digest)
	resp, err := c.makeRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("upstream registry returned status %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, nil
}

// CheckBlobExists checks if a blob exists in the upstream registry
func (c *DockerRegistryProxyClient) CheckBlobExists(ctx context.Context, name, digest string) (bool, int64, error) {
	path := fmt.Sprintf("/v2/%s/blobs/%s", name, digest)
	resp, err := c.makeRequest(ctx, http.MethodHead, path, nil)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, resp.ContentLength, nil
	}

	return false, 0, nil
}
