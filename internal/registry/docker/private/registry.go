package private

import (
	"fmt"
	"net"
	"net/http"

	"github.com/collective-projects/brm-server/internal/storage"
	"github.com/collective-projects/brm-server/pkg/configkeys"
	"github.com/collective-projects/brm-server/pkg/models"
)

// DockerRegistryPrivate implements a private Docker registry that stores artifacts locally
type DockerRegistryPrivate struct {
	models.PrivateRegistry // Embed PrivateRegistry instead of BaseRegistry
	service                *DockerRegistryPrivateService
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
		PrivateRegistry: models.PrivateRegistry{
			BaseRegistry: models.BaseRegistry{
				Alias:              alias,
				RegistryType:       models.RegistryTypePrivate,
				ImplementationType: configkeys.RegistryClassDockerPrivate,
				ServiceBinding:     serviceBinding,
				StorageAlias:       storageAlias,
				Description:        description,
			},
		},
		service: service,
	}

	return registry, nil
}

// SetupRoutes implements Registry interface.
// This is called by RegistryManager to configure routes for this registry.
func (d *DockerRegistryPrivate) SetupRoutes(mux *http.ServeMux) error {
	SetupRoutes(mux, d.service)
	return nil
}

// GetStorageAlias returns the storage alias
func (d *DockerRegistryPrivate) GetStorageAlias() string {
	return d.StorageAlias
}

// GetDescription returns the description
func (d *DockerRegistryPrivate) GetDescription() string {
	return d.Description
}
