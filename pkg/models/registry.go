package models

import (
	"fmt"
	"net"
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

// BaseRegistry provides common functionality for all registry implementations.
type BaseRegistry struct {
	alias              string
	registryType       RegistryType
	implementationType string
}

// Alias returns the alias/name of the registry.
func (b *BaseRegistry) Alias() string {
	return b.alias
}

// SetAlias sets the alias/name of the registry.
func (b *BaseRegistry) SetAlias(alias string) {
	b.alias = alias
}

// Type returns the registry type (private, proxy, or compound).
func (b *BaseRegistry) Type() RegistryType {
	return b.registryType
}

// SetType sets the registry type.
func (b *BaseRegistry) SetType(registryType RegistryType) {
	b.registryType = registryType
}

// ImplementationType returns the implementation type/class name of the registry.
func (b *BaseRegistry) ImplementationType() string {
	return b.implementationType
}

// SetImplementationType sets the implementation type/class name.
func (b *BaseRegistry) SetImplementationType(implementationType string) {
	b.implementationType = implementationType
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
	// Type returns the registry type (private, proxy, or compound).
	Type() RegistryType

	// ImplementationType returns the implementation type/class name of the registry (e.g., "docker.registry.proxy", "raw.registry").
	// This is used by managers to identify the registry implementation type.
	ImplementationType() string

	// Alias returns the alias/name of the registry.
	Alias() string
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
type PrivateRegistry struct {
	// BaseRegistry provides common registry functionality (alias, type, implementationType).
	BaseRegistry

	// StorageAlias is the alias/name of the storage backend registered in StorageManager.
	// The actual ArtifactStorage instance is resolved by looking up this alias.
	StorageAlias string `json:"storageAlias"`

	// ServiceBinding is the network address binding (IP and port) for this registry service.
	ServiceBinding net.Addr `json:"serviceBinding,omitempty"`

	// Description is an optional human-readable description of the registry.
	Description string `json:"description,omitempty"`
}

// ProxyRegistry represents a proxy registry that caches artifacts from upstream registries.
// It implements the Registry interface.
type ProxyRegistry struct {
	// BaseRegistry provides common registry functionality (alias, type, implementationType).
	BaseRegistry

	// StorageAlias is the alias/name of the cache storage backend registered in StorageManager.
	// The actual ArtifactStorage instance is resolved by looking up this alias.
	StorageAlias string `json:"storageAlias"`

	// Upstream is the upstream registry configuration.
	Upstream *UpstreamRegistry `json:"upstream"`

	// ServiceBinding is the network address binding (IP and port) for this registry service.
	ServiceBinding net.Addr `json:"serviceBinding,omitempty"`

	// CacheTTL is the cache expiration time in seconds.
	// After this period, cached artifacts may be refreshed from upstream.
	CacheTTL int64 `json:"cacheTTL,omitempty"`
}

// CompoundRegistry represents a compound registry that combines private storage with proxy registries.
// It implements the Registry interface.
type CompoundRegistry struct {
	// BaseRegistry provides common registry functionality (alias, type, implementationType).
	BaseRegistry

	// PrivateStorageAlias is the alias/name of the private storage backend registered in StorageManager.
	// The actual ArtifactStorage instance is resolved by looking up this alias.
	PrivateStorageAlias string `json:"privateStorageAlias"`

	// Proxies is an ordered list of proxy registries to check when artifacts are not found locally.
	// The order matters: artifacts are checked from proxies in the order they appear in this slice.
	Proxies []*ProxyRegistry `json:"proxies,omitempty"`

	// ServiceBinding is the network address binding (IP and port) for this registry service.
	ServiceBinding net.Addr `json:"serviceBinding,omitempty"`

	// ReadStrategy defines the strategy for reading artifacts.
	// Possible values: "local-first" (check local storage first, then proxies),
	// "proxy-first" (check proxies first, then local), or "local-only" (only local).
	ReadStrategy string `json:"readStrategy,omitempty"`
}
