package proxy

import (
	"fmt"
	"net"
	"net/http"

	"github.com/collective-projects/brm-server/internal/storage"
	"github.com/collective-projects/brm-server/pkg/configkeys"
	"github.com/collective-projects/brm-server/pkg/models"
)

// DockerRegistryProxy implements a Docker registry proxy that caches artifacts from upstream registries
type DockerRegistryProxy struct {
	models.ProxyRegistry // Embed ProxyRegistry instead of BaseRegistry
	service              *DockerRegistryProxyService
}

// NewDockerRegistryProxy creates a new Docker registry proxy instance
func NewDockerRegistryProxy(
	alias string,
	storageAlias string,
	upstream *models.UpstreamRegistry,
	serviceBinding net.Addr,
	cacheTTL int64,
) (*DockerRegistryProxy, error) {
	if storageAlias == "" {
		return nil, fmt.Errorf("storageAlias cannot be empty")
	}
	if upstream == nil {
		return nil, fmt.Errorf("upstream configuration cannot be nil")
	}
	if upstream.URL == "" {
		return nil, fmt.Errorf("upstream URL cannot be empty")
	}

	// Resolve storage from StorageManager
	storageManager := storage.GetManager()
	storageInstance, err := storageManager.Get(storageAlias)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage by alias %s: %w", storageAlias, err)
	}

	// Create service
	service, err := NewDockerRegistryProxyService(storageAlias, upstream, cacheTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry service: %w", err)
	}

	// Set storage in service
	service.SetStorage(storageInstance)

	registry := &DockerRegistryProxy{
		ProxyRegistry: models.ProxyRegistry{
			BaseRegistry: models.BaseRegistry{
				Alias:              alias,
				RegistryType:       models.RegistryTypeProxy,
				ImplementationType: configkeys.RegistryClassDockerProxy,
				ServiceBinding:     serviceBinding,
				StorageAlias:       storageAlias,
			},
			Upstream: upstream,
			CacheTTL: cacheTTL,
		},
		service: service,
	}

	return registry, nil
}

// SetupRoutes implements Registry interface.
// This is called by RegistryManager to configure routes for this registry.
func (d *DockerRegistryProxy) SetupRoutes(mux *http.ServeMux) error {
	SetupRoutes(mux, d.service)
	return nil
}

// GetStorageAlias returns the storage alias
func (d *DockerRegistryProxy) GetStorageAlias() string {
	return d.StorageAlias
}

// GetUpstream returns the upstream registry configuration
func (d *DockerRegistryProxy) GetUpstream() *models.UpstreamRegistry {
	return d.Upstream
}

// GetCacheTTL returns the cache TTL
func (d *DockerRegistryProxy) GetCacheTTL() int64 {
	return d.CacheTTL
}
