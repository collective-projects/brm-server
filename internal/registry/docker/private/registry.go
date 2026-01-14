package private

import (
	"fmt"
	"net"

	"github.com/collective-projects/brm-server/internal/storage"
	"github.com/collective-projects/brm-server/pkg/models"
)

// DockerRegistryPrivate implements a private Docker registry that stores artifacts locally
type DockerRegistryPrivate struct {
	models.BaseRegistry
	storageAlias   string
	serviceBinding net.Addr
	description    string
	service        *DockerRegistryPrivateService
}

// NewDockerRegistryPrivate creates a new private Docker registry instance
func NewDockerRegistryPrivate(
	alias string,
	storageAlias string,
	serviceBinding net.Addr,
	description string,
) (*DockerRegistryPrivate, error) {
	if storageAlias == "" {
		return nil, fmt.Errorf("storageAlias cannot be empty")
	}

	// Resolve storage from StorageManager
	storageManager := storage.GetManager()
	storageInstance, err := storageManager.Get(storageAlias)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage by alias %s: %w", storageAlias, err)
	}

	// Create service
	service, err := NewDockerRegistryPrivateService(storageAlias, description)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry service: %w", err)
	}

	// Set storage in service
	service.SetStorage(storageInstance)

	registry := &DockerRegistryPrivate{
		storageAlias:   storageAlias,
		serviceBinding: serviceBinding,
		description:    description,
		service:        service,
	}
	registry.BaseRegistry.SetAlias(alias)
	registry.BaseRegistry.SetType(models.RegistryTypePrivate)
	registry.BaseRegistry.SetImplementationType("docker.registry.private")

	return registry, nil
}

// Service returns the Docker registry service instance
func (d *DockerRegistryPrivate) Service() *DockerRegistryPrivateService {
	return d.service
}

// GetStorageAlias returns the storage alias
func (d *DockerRegistryPrivate) GetStorageAlias() string {
	return d.storageAlias
}

// GetServiceBinding returns the service binding
func (d *DockerRegistryPrivate) GetServiceBinding() net.Addr {
	return d.serviceBinding
}

// GetDescription returns the description
func (d *DockerRegistryPrivate) GetDescription() string {
	return d.description
}
