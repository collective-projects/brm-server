package registry

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/collective-projects/brm-server/pkg/models"

	"github.com/collective-projects/brm-config/pkg/config"
	"github.com/collective-projects/brm-server/internal/registry/docker/private"
	"github.com/collective-projects/brm-server/internal/registry/docker/proxy"
	"github.com/collective-projects/brm-server/pkg/configkeys"
)

var (
	defaultManager *RegistryManager
	managerOnce    sync.Once
)

// RegistryManager manages registry instances and their factory functions
type RegistryManager struct {
	registries map[string]models.Registry
	factories  map[string]func(...interface{}) (models.Registry, error)
	mu         sync.RWMutex
}

// GetManager returns the singleton RegistryManager instance
func GetManager() *RegistryManager {
	managerOnce.Do(func() {
		defaultManager = &RegistryManager{
			registries: make(map[string]models.Registry),
			factories:  make(map[string]func(...interface{}) (models.Registry, error)),
		}
		// Register built-in factories
		defaultManager.init()
	})
	return defaultManager
}

// init registers built-in registry factory functions
func (rm *RegistryManager) init() {
	// Register Docker registry factory
	// Parameters: [alias, serviceBinding, storageAlias, upstream, cacheTTL]
	rm.RegisterFactory(configkeys.RegistryClassDockerProxy, func(params ...interface{}) (models.Registry, error) {
		if len(params) < 3 {
			return nil, fmt.Errorf("docker.registry requires at least alias, serviceBinding, storageAlias, and upstream parameters")
		}

		alias, ok := params[0].(string)
		if !ok {
			return nil, fmt.Errorf("docker.registry alias must be a string")
		}

		var serviceBinding net.Addr
		if params[1] != nil {
			serviceBinding, ok = params[1].(net.Addr)
			if !ok {
				return nil, fmt.Errorf("docker.registry serviceBinding must be net.Addr")
			}
		}

		storageAlias, ok := params[2].(string)
		if !ok {
			return nil, fmt.Errorf("docker.registry storageAlias must be a string")
		}

		upstream, ok := params[3].(*models.UpstreamRegistry)
		if !ok {
			return nil, fmt.Errorf("docker.registry upstream must be *models.UpstreamRegistry")
		}

		var cacheTTL int64
		if len(params) >= 5 {
			cacheTTL, ok = params[4].(int64)
			if !ok {
				return nil, fmt.Errorf("docker.registry cacheTTL must be int64")
			}
		}

		return proxy.NewDockerRegistryProxy(alias, storageAlias, upstream, serviceBinding, cacheTTL)
	})

	// Register Docker private registry factory
	// Parameters: [alias, serviceBinding, storageAlias, description]
	rm.RegisterFactory(configkeys.RegistryClassDockerPrivate, func(params ...interface{}) (models.Registry, error) {
		if len(params) < 2 {
			return nil, fmt.Errorf("docker.registry.private requires at least alias and serviceBinding parameters")
		}

		alias, ok := params[0].(string)
		if !ok {
			return nil, fmt.Errorf("docker.registry.private alias must be a string")
		}

		var serviceBinding net.Addr
		if params[1] != nil {
			serviceBinding, ok = params[1].(net.Addr)
			if !ok {
				return nil, fmt.Errorf("docker.registry.private serviceBinding must be net.Addr")
			}
		}

		storageAlias, ok := params[2].(string)
		if !ok {
			return nil, fmt.Errorf("docker.registry.private storageAlias must be a string")
		}

		var description string
		if len(params) >= 4 {
			description, ok = params[3].(string)
			if !ok {
				return nil, fmt.Errorf("docker.registry.private description must be a string")
			}
		}

		return private.NewDockerRegistryPrivate(alias, storageAlias, serviceBinding, description)
	})
}

// isValidAlias validates that a string is a valid alias for configuration
// Rules: lowercase only, must start with a letter, can contain letters, numbers, underscores, and dashes
// Max length: 63 characters (to be compatible with various naming conventions)
func isValidAlias(name string) bool {
	// Check if empty
	if len(name) == 0 {
		return false
	}

	// Check length (max 63 characters)
	if len(name) > 63 {
		return false
	}

	// Regex pattern for valid alias
	// ^[a-z]([a-z0-9_-]*[a-z0-9])?$
	// This ensures:
	// - Starts with a lowercase letter
	// - Can contain lowercase letters, numbers, underscores, and hyphens
	// - Ends with alphanumeric (if more than 1 character)
	aliasPattern := regexp.MustCompile(`^[a-z]([a-z0-9_-]*[a-z0-9])?$`)
	return aliasPattern.MatchString(name)
}

// RegisterFactory registers a registry factory function for the given class name
func (rm *RegistryManager) RegisterFactory(className string, factory func(...interface{}) (models.Registry, error)) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.factories[className] = factory
}

// Create creates a new registry instance with the given class name and alias
// The alias must be a valid alias (lowercase, starting with letter, alphanumeric with underscore/dash).
// The params are passed to the factory function. serviceBinding is optional and can be nil.
func (rm *RegistryManager) Create(className, alias string, serviceBinding net.Addr, params ...interface{}) (models.Registry, error) {
	// Validate alias format
	if !isValidAlias(alias) {
		return nil, fmt.Errorf("invalid alias format: %s (must be lowercase, start with letter, contain only letters, numbers, underscores, and dashes)", alias)
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Check if alias already exists
	if _, exists := rm.registries[alias]; exists {
		return nil, fmt.Errorf("registry alias already exists: %s", alias)
	}

	// Look up factory
	factory, exists := rm.factories[className]
	if !exists {
		return nil, fmt.Errorf("registry class not found: %s", className)
	}

	// Create registry instance (pass alias and serviceBinding as first parameters)
	registry, err := factory(append([]interface{}{alias, serviceBinding}, params...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry instance: %w", err)
	}

	// Store instance
	rm.registries[alias] = registry

	return registry, nil
}

// convertServiceBinding converts net.Addr to *models.ServiceBinding
func (rm *RegistryManager) convertServiceBinding(addr net.Addr) *models.ServiceBinding {
	if addr == nil {
		return nil
	}
	if sb, ok := addr.(*models.ServiceBinding); ok {
		return sb
	}
	// Try to parse from String() format "ip:port"
	addrStr := addr.String()
	host, portStr, err := net.SplitHostPort(addrStr)
	if err != nil {
		return nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil
	}
	return &models.ServiceBinding{
		IP:   host,
		Port: port,
	}
}

// SaveToConfig serializes all registry configurations to a map by extracting from instances
func (rm *RegistryManager) SaveToConfig() map[string]interface{} {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	result := make(map[string]interface{})
	for alias, registry := range rm.registries {
		regConfig := map[string]interface{}{
			configkeys.KeyClass: registry.ImplementationType(),
			configkeys.KeyAlias: registry.Alias(),
		}

		// Extract implementation-specific config
		switch impl := registry.(type) {
		case *private.DockerRegistryPrivate:
			params := map[string]interface{}{
				configkeys.KeyStorageAlias: impl.GetStorageAlias(),
			}
			if desc := impl.GetDescription(); desc != "" {
				params[configkeys.KeyDescription] = desc
			}
			regConfig[configkeys.KeyParams] = params
			if sb := rm.convertServiceBinding(impl.GetServiceBinding()); sb != nil {
				regConfig[configkeys.KeyServiceBinding] = sb
			}

		case *proxy.DockerRegistryProxy:
			params := map[string]interface{}{
				configkeys.KeyStorageAlias: impl.GetStorageAlias(),
			}
			if upstream := impl.GetUpstream(); upstream != nil {
				params[configkeys.KeyUpstream] = upstream
			}
			if cacheTTL := impl.GetCacheTTL(); cacheTTL > 0 {
				params[configkeys.KeyCacheTTL] = cacheTTL
			}
			regConfig[configkeys.KeyParams] = params
			if sb := rm.convertServiceBinding(impl.GetServiceBinding()); sb != nil {
				regConfig[configkeys.KeyServiceBinding] = sb
			}

		default:
			// Unknown implementation type - skip or log warning
			continue
		}

		result[alias] = regConfig
	}
	return result
}

// LoadFromConfig creates registry instances from configuration
// Expected format: registry: { alias1: {class: ..., serviceBinding: ..., params: ...}, alias2: {...} }
func (rm *RegistryManager) LoadFromConfig(cfg *config.Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	registryConfig := cfg.GetSubConfig(configkeys.SectionRegistry)
	if registryConfig == nil {
		logger.Info("No registry configuration found; skipping registry initialization")
		return nil
	}

	// Get all keys under registry (each key is an alias)
	aliases := registryConfig.Keys()
	logger.Info("Registry aliases discovered from configuration", "count", len(aliases), "aliases", aliases)
	for _, alias := range aliases {
		instanceConfig := registryConfig.GetSubConfig(alias)
		if instanceConfig == nil {
			logger.Warn("Registry alias config missing; skipping", "alias", alias)
			continue
		}

		if err := rm.createRegistryFromConfig(alias, instanceConfig, logger); err != nil {
			return fmt.Errorf("failed to create registry instance %s: %w", alias, err)
		}
	}

	return nil
}

// createRegistryFromConfig creates a registry from a config sub-tree
func (rm *RegistryManager) createRegistryFromConfig(alias string, registryConfig *config.Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	className := registryConfig.GetString(configkeys.KeyClass)
	if className == "" {
		return fmt.Errorf("registry %s: class is required", alias)
	}

	// Extract service binding
	var serviceBinding net.Addr
	if registryConfig.Exists(configkeys.KeyServiceBinding) {
		sbConfig := registryConfig.GetSubConfig(configkeys.KeyServiceBinding)
		ip := sbConfig.GetString(configkeys.KeyIP)
		port := sbConfig.GetInt(configkeys.KeyPort)
		if ip != "" && port > 0 {
			serviceBinding = &models.ServiceBinding{
				IP:   ip,
				Port: port,
			}
		}
	}

	// Extract parameters based on class
	paramsConfig := registryConfig.GetSubConfig(configkeys.KeyParams)
	var params []interface{}

	switch className {
	case configkeys.RegistryClassDockerProxy:
		storageAlias := paramsConfig.GetString(configkeys.KeyStorageAlias)
		if storageAlias == "" {
			return fmt.Errorf("registry %s: storageAlias is required", alias)
		}

		// Extract upstream
		if !paramsConfig.Exists(configkeys.KeyUpstream) {
			return fmt.Errorf("registry %s: upstream is required", alias)
		}
		upstreamConfig := paramsConfig.GetSubConfig(configkeys.KeyUpstream)
		upstreamURL := upstreamConfig.GetString(configkeys.KeyURL)
		if upstreamURL == "" {
			return fmt.Errorf("registry %s: upstream.url is required", alias)
		}
		upstream := &models.UpstreamRegistry{
			URL:      upstreamURL,
			Username: upstreamConfig.GetString(configkeys.KeyUsername),
			Password: upstreamConfig.GetString(configkeys.KeyPassword),
			TTL:      int64(upstreamConfig.GetInt(configkeys.KeyTTL)),
		}

		cacheTTL := int64(paramsConfig.GetInt(configkeys.KeyCacheTTL))
		params = []interface{}{storageAlias, upstream, cacheTTL}

	case configkeys.RegistryClassDockerPrivate:
		storageAlias := paramsConfig.GetString(configkeys.KeyStorageAlias)
		if storageAlias == "" {
			return fmt.Errorf("registry %s: storageAlias is required", alias)
		}
		description := paramsConfig.GetString(configkeys.KeyDescription)
		params = []interface{}{storageAlias, description}

	default:
		return fmt.Errorf("registry %s: unknown class %s", alias, className)
	}

	// Create registry instance
	_, err := rm.Create(className, alias, serviceBinding, params...)
	if err != nil {
		return fmt.Errorf("failed to create registry %s: %w", alias, err)
	}

	// Avoid logging secrets (upstream password). Only log safe fields.
	switch className {
	case configkeys.RegistryClassDockerProxy:
		var storageAlias string
		if len(params) >= 1 {
			storageAlias, _ = params[0].(string)
		}
		var upstreamURL string
		var upstreamUsername string
		if len(params) >= 2 {
			if up, ok := params[1].(*models.UpstreamRegistry); ok && up != nil {
				upstreamURL = up.URL
				upstreamUsername = up.Username
			}
		}
		var cacheTTL interface{}
		if len(params) >= 3 {
			cacheTTL = params[2]
		}
		logger.Info("Registry instance created",
			"alias", alias,
			"class", className,
			"serviceBinding", serviceBinding,
			"storageAlias", storageAlias,
			"upstreamURL", upstreamURL,
			"upstreamUsername", upstreamUsername,
			"cacheTTL", cacheTTL,
		)
	case configkeys.RegistryClassDockerPrivate:
		var storageAlias string
		if len(params) >= 1 {
			storageAlias, _ = params[0].(string)
		}
		var description string
		if len(params) >= 2 {
			description, _ = params[1].(string)
		}
		logger.Info("Registry instance created",
			"alias", alias,
			"class", className,
			"serviceBinding", serviceBinding,
			"storageAlias", storageAlias,
			"description", description,
		)
	default:
		logger.Info("Registry instance created", "alias", alias, "class", className, "serviceBinding", serviceBinding)
	}
	return nil
}

// ServerInfo tracks an HTTP server and its associated registry information
type ServerInfo struct {
	Server        *http.Server
	RegistryAlias string
	Address       string
}

// StartAllServers starts HTTP servers for all registered registries.
// Each registry is served on its own HTTP server bound to its ServiceBinding address.
// Returns a slice of ServerInfo for graceful shutdown coordination.
func (rm *RegistryManager) StartAllServers(readTimeout, writeTimeout, idleTimeout time.Duration, logger *slog.Logger) ([]*ServerInfo, error) {
	rm.mu.RLock()
	registries := make([]models.Registry, 0, len(rm.registries))
	for _, registry := range rm.registries {
		registries = append(registries, registry)
	}
	rm.mu.RUnlock()

	var servers []*ServerInfo
	addresses := make(map[string]bool) // Track addresses to detect conflicts

	for _, registry := range registries {
		// Get ServiceBinding
		var serviceBinding net.Addr
		switch impl := registry.(type) {
		case *private.DockerRegistryPrivate:
			serviceBinding = impl.GetServiceBinding()
		case *proxy.DockerRegistryProxy:
			serviceBinding = impl.GetServiceBinding()
		default:
			logger.Warn("Registry does not support HTTP serving", "alias", registry.Alias(), "type", registry.ImplementationType())
			continue
		}

		if serviceBinding == nil {
			logger.Warn("Registry has no service binding, skipping", "alias", registry.Alias())
			continue
		}

		address := serviceBinding.String()

		// Check for port conflicts
		if addresses[address] {
			return nil, fmt.Errorf("port conflict: multiple registries configured for address %s", address)
		}
		addresses[address] = true

		// Create HTTP mux and setup routes based on implementation type
		mux := http.NewServeMux()
		implType := registry.ImplementationType()

		switch implType {
		case configkeys.RegistryClassDockerPrivate:
			privateReg, ok := registry.(*private.DockerRegistryPrivate)
			if !ok {
				return nil, fmt.Errorf("registry %s: type assertion failed for docker.registry.private", registry.Alias())
			}
			service := privateReg.Service()
			private.SetupRoutes(mux, service)

		case configkeys.RegistryClassDockerProxy:
			proxyReg, ok := registry.(*proxy.DockerRegistryProxy)
			if !ok {
				return nil, fmt.Errorf("registry %s: type assertion failed for docker.registry", registry.Alias())
			}
			service := proxyReg.Service()
			proxy.SetupRoutes(mux, service)

		default:
			logger.Warn("Unknown registry implementation type, skipping", "alias", registry.Alias(), "type", implType)
			continue
		}

		// Create HTTP server
		server := &http.Server{
			Addr:         address,
			Handler:      mux,
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
			IdleTimeout:  idleTimeout,
		}

		// Start server in goroutine
		go func(srv *http.Server, alias, addr string) {
			logger.Info("Starting registry HTTP server", "alias", alias, "address", addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("Registry HTTP server failed", "alias", alias, "address", addr, "error", err)
			}
		}(server, registry.Alias(), address)

		servers = append(servers, &ServerInfo{
			Server:        server,
			RegistryAlias: registry.Alias(),
			Address:       address,
		})
	}

	if len(servers) > 0 {
		logger.Info("Started HTTP servers for registries", "count", len(servers))
		for _, srv := range servers {
			logger.Info("Registry server running", "alias", srv.RegistryAlias, "address", srv.Address)
		}
	} else {
		logger.Warn("No registries with valid service bindings found")
	}

	return servers, nil
}
