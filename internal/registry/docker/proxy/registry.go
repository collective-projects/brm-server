package proxy

import (
	"fmt"
	"net"

	"github.com/collective-projects/brm-server/pkg/configkeys"
	"github.com/collective-projects/brm-server/pkg/models"

	"github.com/collective-projects/brm-server/internal/storage"
)

// DockerRegistryProxy implements a Docker registry proxy that caches artifacts from upstream registries
type DockerRegistryProxy struct {
	models.BaseRegistry
	storageAlias   string
	upstream       *models.UpstreamRegistry
	serviceBinding net.Addr
	cacheTTL       int64
	service        *DockerRegistryProxyService
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
		storageAlias:   storageAlias,
		upstream:       upstream,
		serviceBinding: serviceBinding,
		cacheTTL:       cacheTTL,
		service:        service,
	}
	registry.BaseRegistry.SetAlias(alias)
	registry.BaseRegistry.SetType(models.RegistryTypeProxy)
	registry.BaseRegistry.SetImplementationType(configkeys.RegistryClassDockerProxy)

	return registry, nil
}

// Service returns the Docker registry service instance
func (d *DockerRegistryProxy) Service() *DockerRegistryProxyService {
	return d.service
}

// GetStorageAlias returns the storage alias
func (d *DockerRegistryProxy) GetStorageAlias() string {
	return d.storageAlias
}

// GetUpstream returns the upstream registry configuration
func (d *DockerRegistryProxy) GetUpstream() *models.UpstreamRegistry {
	return d.upstream
}

// GetServiceBinding returns the service binding
func (d *DockerRegistryProxy) GetServiceBinding() net.Addr {
	return d.serviceBinding
}

// GetCacheTTL returns the cache TTL
func (d *DockerRegistryProxy) GetCacheTTL() int64 {
	return d.cacheTTL
}
