package models

import (
	"fmt"
	"net"
	"net/http"
)

// ServiceBinding represents a network address binding (IP and port) for a registry service.
// It implements the net.Addr interface.
type ServiceBinding struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// Network returns the network type (always "tcp" for ServiceBinding).
func (s *ServiceBinding) Network() string {
	return "tcp"
}

// String returns the string representation of the address (e.g., "0.0.0.0:5000").
func (s *ServiceBinding) String() string {
	return fmt.Sprintf("%s:%d", s.IP, s.Port)
}

// RegistryType represents the type of registry.
type RegistryType string

const (
	// RegistryTypePrivate represents a private registry that stores artifacts locally.
	RegistryTypePrivate RegistryType = "private"

	// RegistryTypeProxy represents a proxy registry that caches artifacts from upstream registries.
	RegistryTypeProxy RegistryType = "proxy"

	// RegistryTypeCompound represents a compound registry that combines private storage with proxy registries.
	RegistryTypeCompound RegistryType = "compound"
)

// Registry is the base interface for all registry types.
type Registry interface {
	// SetupRoutes configures HTTP routes for this registry on the given mux.
	// This allows each registry implementation to manage its own routing and handlers internally.
	// Returns an error if route setup fails.
	SetupRoutes(mux *http.ServeMux) error
}

// BaseRegistry provides common fields for all registry implementations.
// Fields are exported so they can be accessed directly through embedding.
type BaseRegistry struct {
	// Alias is the unique identifier/name for this registry instance.
	Alias string

	// RegistryType indicates whether this is a private, proxy, or compound registry.
	RegistryType RegistryType

	// ImplementationType is the implementation class name (e.g., "docker.registry.private", "raw.registry").
	ImplementationType string

	// ServiceBinding is the network address (IP:port) where this registry's HTTP server listens.
	ServiceBinding net.Addr

	// StorageAlias is the alias/name of the storage backend registered in StorageManager.
	// The actual ArtifactStorage instance is resolved by looking up this alias.
	StorageAlias string

	// Description is an optional human-readable description of the registry.
	Description string
}

// SetupRoutes provides a default implementation that returns "not implemented" error.
// Concrete implementations must override this to handle HTTP requests.
func (b *BaseRegistry) SetupRoutes(mux *http.ServeMux) error {
	return fmt.Errorf("SetupRoutes not implemented for registry %s", b.Alias)
}


// UpstreamRegistry represents the configuration for an upstream registry used by proxy registries.
type UpstreamRegistry struct {
	// URL is the base URL of the upstream registry (e.g., "https://registry-1.docker.io").
	URL string `json:"url"`

	// Username is the optional authentication username for accessing the upstream registry.
	Username string `json:"username,omitempty"`

	// Password is the optional authentication password for accessing the upstream registry.
	// Note: In production, consider using secure credential storage instead of plain text.
	Password string `json:"password,omitempty"`

	// TTL is the cache time-to-live in seconds. After this period, cached artifacts may be refreshed.
	// If 0, uses default TTL (typically 168 hours / 604800 seconds).
	TTL int64 `json:"ttl,omitempty"`
}

// PrivateRegistry represents a private registry that stores artifacts locally.
// It implements the Registry interface.
// Inherits StorageAlias and Description from BaseRegistry.
type PrivateRegistry struct {
	// BaseRegistry provides common registry functionality (alias, type, implementationType, serviceBinding, storageAlias, description).
	BaseRegistry
}

// ProxyRegistry represents a proxy registry that caches artifacts from upstream registries.
// It implements the Registry interface.
// Inherits StorageAlias (for cache) and Description from BaseRegistry.
type ProxyRegistry struct {
	// BaseRegistry provides common registry functionality (alias, type, implementationType, serviceBinding, storageAlias, description).
	// For ProxyRegistry, StorageAlias refers to the cache storage.
	BaseRegistry

	// Upstream is the upstream registry configuration.
	Upstream *UpstreamRegistry `json:"upstream"`

	// CacheTTL is the cache expiration time in seconds.
	// After this period, cached artifacts may be refreshed from upstream.
	CacheTTL int64 `json:"cacheTTL,omitempty"`
}

// CompoundRegistry represents a compound registry that combines private storage with proxy registries.
// It implements the Registry interface.
type CompoundRegistry struct {
	// BaseRegistry provides common registry functionality (alias, type, implementationType, serviceBinding, storageAlias, description).
	// For CompoundRegistry, StorageAlias refers to the private storage component.
	BaseRegistry

	// PrivateRegistry is the embedded private registry component for local artifact storage.
	PrivateRegistry *PrivateRegistry `json:"privateRegistry"`

	// Proxies is an ordered list of proxy registries to check when artifacts are not found locally.
	// The order matters: artifacts are checked from proxies in the order they appear in this slice.
	Proxies []*ProxyRegistry `json:"proxies,omitempty"`

	// ReadStrategy defines the strategy for reading artifacts.
	// Possible values: "local-first" (check local storage first, then proxies),
	// "proxy-first" (check proxies first, then local), or "local-only" (only local).
	ReadStrategy string `json:"readStrategy,omitempty"`
}
