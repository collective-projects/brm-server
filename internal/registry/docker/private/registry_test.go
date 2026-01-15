package private

import (
	"net"
	"testing"

	"github.com/collective-projects/brm-server/pkg/configkeys"
	"github.com/collective-projects/brm-server/internal/storage"
	"github.com/collective-projects/brm-server/pkg/models"
)

// setupTestRegistry creates a test registry instance
func setupTestRegistry(t *testing.T) *DockerRegistryPrivate {
	// Create test storage
	baseDir := t.TempDir()
	_, err := storage.NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create test storage: %v", err)
	}

	// Register storage in manager
	storageManager := storage.GetManager()
	_, err = storageManager.Create(configkeys.StorageClassStdFile, "test-storage", baseDir)
	if err != nil {
		// May already exist, that's okay
	}

	// Create service binding
	serviceBinding := &models.ServiceBinding{
		IP:   "127.0.0.1",
		Port: 5000,
	}

	// Create registry
	registry, err := NewDockerRegistryPrivate(
		"test-registry",
		"test-storage",
		serviceBinding,
		"test description",
	)
	if err != nil {
		t.Fatalf("Failed to create registry: %v", err)
	}

	return registry
}

// TestNewDockerRegistryPrivate tests registry creation
func TestNewDockerRegistryPrivate(t *testing.T) {
	baseDir := t.TempDir()
	_, err := storage.NewSimpleFileStorage("test-storage", baseDir)
	if err != nil {
		t.Fatalf("Failed to create test storage: %v", err)
	}

	storageManager := storage.GetManager()
	_, err = storageManager.Create(configkeys.StorageClassStdFile, "test-storage", baseDir)
	if err != nil {
		// May already exist
	}

	serviceBinding := &models.ServiceBinding{
		IP:   "0.0.0.0",
		Port: 5000,
	}

	registry, err := NewDockerRegistryPrivate(
		"test-registry",
		"test-storage",
		serviceBinding,
		"test description",
	)
	if err != nil {
		t.Fatalf("NewDockerRegistryPrivate failed: %v", err)
	}

	// Verify registry properties
	if registry.Type() != models.RegistryTypePrivate {
		t.Errorf("Expected type %v, got %v", models.RegistryTypePrivate, registry.Type())
	}

	if registry.ImplementationType() != configkeys.RegistryClassDockerPrivate {
		t.Errorf("Expected implementation type %q, got %s", configkeys.RegistryClassDockerPrivate, registry.ImplementationType())
	}

	if registry.Alias() != "test-registry" {
		t.Errorf("Expected alias 'test-registry', got %s", registry.Alias())
	}

	if registry.Service() == nil {
		t.Error("Service should not be nil")
	}
}

// TestNewDockerRegistryPrivateEmptyStorageAlias tests error handling
func TestNewDockerRegistryPrivateEmptyStorageAlias(t *testing.T) {
	serviceBinding := &models.ServiceBinding{
		IP:   "0.0.0.0",
		Port: 5000,
	}

	_, err := NewDockerRegistryPrivate(
		"test-registry",
		"", // Empty storage alias
		serviceBinding,
		"test description",
	)
	if err == nil {
		t.Error("Expected error for empty storage alias, got nil")
	}
}

// TestDockerRegistryPrivateType tests Type() method
func TestDockerRegistryPrivateType(t *testing.T) {
	registry := setupTestRegistry(t)

	if registry.Type() != models.RegistryTypePrivate {
		t.Errorf("Expected %v, got %v", models.RegistryTypePrivate, registry.Type())
	}
}

// TestDockerRegistryPrivateImplementationType tests ImplementationType() method
func TestDockerRegistryPrivateImplementationType(t *testing.T) {
	registry := setupTestRegistry(t)

	expected := configkeys.RegistryClassDockerPrivate
	if registry.ImplementationType() != expected {
		t.Errorf("Expected %s, got %s", expected, registry.ImplementationType())
	}
}

// TestDockerRegistryPrivateAlias tests Alias() method
func TestDockerRegistryPrivateAlias(t *testing.T) {
	registry := setupTestRegistry(t)

	expected := "test-registry"
	if registry.Alias() != expected {
		t.Errorf("Expected %s, got %s", expected, registry.Alias())
	}
}

// TestDockerRegistryPrivateServiceBinding tests ServiceBinding field
func TestDockerRegistryPrivateServiceBinding(t *testing.T) {
	registry := setupTestRegistry(t)

	if registry.serviceBinding == nil {
		t.Error("ServiceBinding should not be nil")
	}

	// Verify it implements net.Addr
	_, ok := registry.serviceBinding.(net.Addr)
	if !ok {
		t.Error("ServiceBinding should implement net.Addr")
	}
}
