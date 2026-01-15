package storage

import (
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/collective-projects/brm-config/pkg/config"
	"github.com/collective-projects/brm-server/pkg/configkeys"
	"github.com/collective-projects/brm-server/pkg/models"
)

var (
	defaultManager *StorageManager
	managerOnce    sync.Once
)

// StorageConfig holds the configuration for a storage instance
type StorageConfig struct {
	Class  string                 `json:"class"`
	Alias  string                 `json:"alias"`
	Params map[string]interface{} `json:"params"`
}

// StorageManager manages storage instances and their factory functions
type StorageManager struct {
	storages  map[string]models.ArtifactStorage
	configs   map[string]*StorageConfig // Track configuration for each storage
	factories map[string]func(...interface{}) (models.ArtifactStorage, error)
	mu        sync.RWMutex
}

// GetManager returns the singleton StorageManager instance
func GetManager() *StorageManager {
	managerOnce.Do(func() {
		defaultManager = &StorageManager{
			storages:  make(map[string]models.ArtifactStorage),
			configs:   make(map[string]*StorageConfig),
			factories: make(map[string]func(...interface{}) (models.ArtifactStorage, error)),
		}
		// Register built-in factories
		defaultManager.init()
	})
	return defaultManager
}

// init registers built-in storage factory functions
func (sm *StorageManager) init() {
	// Register SimpleFileStorage factory
	// Parameters: [alias, basePath]
	sm.RegisterFactory(configkeys.StorageClassStdFile, func(params ...interface{}) (models.ArtifactStorage, error) {
		if len(params) < 2 {
			return nil, fmt.Errorf("filestorage requires alias and basePath parameters")
		}
		alias, ok := params[0].(string)
		if !ok {
			return nil, fmt.Errorf("filestorage alias must be a string")
		}
		basePath, ok := params[1].(string)
		if !ok {
			return nil, fmt.Errorf("filestorage basePath must be a string")
		}
		return NewSimpleFileStorage(alias, basePath)
	})

	// Register ConcurrentArtifactStorage factory
	// Parameters: [alias, baseDir, lockDir, lockTimeout]
	sm.RegisterFactory(configkeys.StorageClassConcurrentFile, func(params ...interface{}) (models.ArtifactStorage, error) {
		if len(params) < 4 {
			return nil, fmt.Errorf("concurrent.filestorage requires alias, baseDir, lockDir, and lockTimeout parameters")
		}

		alias, ok := params[0].(string)
		if !ok {
			return nil, fmt.Errorf("concurrent.filestorage alias must be a string")
		}

		baseDir, ok := params[1].(string)
		if !ok {
			return nil, fmt.Errorf("concurrent.filestorage baseDir must be a string")
		}

		lockDir, ok := params[2].(string)
		if !ok {
			return nil, fmt.Errorf("concurrent.filestorage lockDir must be a string")
		}

		lockTimeout, ok := params[3].(time.Duration)
		if !ok {
			return nil, fmt.Errorf("concurrent.filestorage lockTimeout must be a time.Duration")
		}

		// Create underlying SimpleFileStorage
		storage, err := NewSimpleFileStorage(alias, baseDir)
		if err != nil {
			return nil, fmt.Errorf("failed to create underlying storage: %w", err)
		}

		// Wrap with ConcurrentArtifactStorage (alias is already set on SimpleFileStorage)
		return NewConcurrentArtifactStorage(storage, lockDir, lockTimeout)
	})

	// Register HashComputingArtifactStorage factory
	// Parameters: [alias, baseDir] or [alias, baseDir, lockDir, lockTimeout]
	// If 2 parameters: wraps SimpleFileStorage
	// If 4 parameters: wraps ConcurrentArtifactStorage
	sm.RegisterFactory(configkeys.StorageClassHashComputingFile, func(params ...interface{}) (models.ArtifactStorage, error) {
		if len(params) < 2 {
			return nil, fmt.Errorf("hashcomputing.filestorage requires at least alias and baseDir parameters")
		}

		alias, ok := params[0].(string)
		if !ok {
			return nil, fmt.Errorf("hashcomputing.filestorage alias must be a string")
		}

		baseDir, ok := params[1].(string)
		if !ok {
			return nil, fmt.Errorf("hashcomputing.filestorage baseDir must be a string")
		}

		var underlyingStorage models.ArtifactStorage
		var err error

		if len(params) == 2 {
			// Simple file storage only
			underlyingStorage, err = NewSimpleFileStorage(alias, baseDir)
			if err != nil {
				return nil, fmt.Errorf("failed to create underlying storage: %w", err)
			}
		} else if len(params) == 4 {
			// Concurrent file storage with locking
			lockDir, ok := params[2].(string)
			if !ok {
				return nil, fmt.Errorf("hashcomputing.filestorage lockDir must be a string")
			}

			lockTimeout, ok := params[3].(time.Duration)
			if !ok {
				return nil, fmt.Errorf("hashcomputing.filestorage lockTimeout must be a time.Duration")
			}

			// Create SimpleFileStorage with alias
			simpleStorage, err := NewSimpleFileStorage(alias, baseDir)
			if err != nil {
				return nil, fmt.Errorf("failed to create underlying storage: %w", err)
			}

			// Wrap with ConcurrentArtifactStorage (alias is already set on SimpleFileStorage)
			underlyingStorage, err = NewConcurrentArtifactStorage(simpleStorage, lockDir, lockTimeout)
			if err != nil {
				return nil, fmt.Errorf("failed to create concurrent storage: %w", err)
			}
		} else {
			return nil, fmt.Errorf("hashcomputing.filestorage requires 2 parameters (alias, baseDir) or 4 parameters (alias, baseDir, lockDir, lockTimeout)")
		}

		// Wrap with HashComputingArtifactStorage (alias is already set on innermost storage)
		return NewHashComputingArtifactStorage(underlyingStorage), nil
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

// RegisterFactory registers a storage factory function for the given class name
func (sm *StorageManager) RegisterFactory(className string, factory func(...interface{}) (models.ArtifactStorage, error)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.factories[className] = factory
}

// Create creates a new storage instance with the given class name and alias
// The alias must be a valid alias (lowercase, starting with letter, alphanumeric with underscore/dash).
// The params are passed to the factory function.
func (sm *StorageManager) Create(className, alias string, params ...interface{}) (models.ArtifactStorage, error) {
	// Validate alias format
	if !isValidAlias(alias) {
		return nil, fmt.Errorf("invalid alias format: %s (must be lowercase, start with letter, contain only letters, numbers, underscores, and dashes)", alias)
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check if alias already exists
	if _, exists := sm.storages[alias]; exists {
		return nil, fmt.Errorf("storage alias already exists: %s", alias)
	}

	// Look up factory
	factory, exists := sm.factories[className]
	if !exists {
		return nil, fmt.Errorf("storage class not found: %s", className)
	}

	// Create storage instance (pass alias as first parameter)
	storage, err := factory(append([]interface{}{alias}, params...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage instance: %w", err)
	}

	// Store instance
	sm.storages[alias] = storage

	// Store configuration
	sm.configs[alias] = &StorageConfig{
		Class:  className,
		Alias:  alias,
		Params: sm.extractParams(className, params),
	}

	return storage, nil
}

// extractParams extracts configuration parameters based on storage class
// Note: params here are the parameters passed to Create (not including alias)
func (sm *StorageManager) extractParams(className string, params []interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	switch className {
	case configkeys.StorageClassStdFile:
		// Factory receives: [alias, basePath]
		// params passed to Create: [basePath]
		if len(params) >= 1 {
			if basePath, ok := params[0].(string); ok {
				result[configkeys.KeyBasePath] = basePath
			}
		}
	case configkeys.StorageClassConcurrentFile:
		// Factory receives: [alias, baseDir, lockDir, lockTimeout]
		// params passed to Create: [baseDir, lockDir, lockTimeout]
		if len(params) >= 3 {
			if baseDir, ok := params[0].(string); ok {
				result[configkeys.KeyBaseDir] = baseDir
			}
			if lockDir, ok := params[1].(string); ok {
				result[configkeys.KeyLockDir] = lockDir
			}
			if lockTimeout, ok := params[2].(time.Duration); ok {
				result[configkeys.KeyLockTimeout] = lockTimeout.String()
			}
		}
	case configkeys.StorageClassHashComputingFile:
		// Factory receives: [alias, baseDir] or [alias, baseDir, lockDir, lockTimeout]
		// params passed to Create: [baseDir] or [baseDir, lockDir, lockTimeout]
		if len(params) >= 1 {
			if baseDir, ok := params[0].(string); ok {
				result[configkeys.KeyBaseDir] = baseDir
			}
		}
		if len(params) >= 3 {
			if lockDir, ok := params[1].(string); ok {
				result[configkeys.KeyLockDir] = lockDir
			}
			if lockTimeout, ok := params[2].(time.Duration); ok {
				result[configkeys.KeyLockTimeout] = lockTimeout.String()
			}
		}
	}

	return result
}

// SaveToConfig serializes all storage configurations to a map
func (sm *StorageManager) SaveToConfig() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]interface{})
	for alias, cfg := range sm.configs {
		result[alias] = map[string]interface{}{
			configkeys.KeyClass:  cfg.Class,
			configkeys.KeyAlias:  cfg.Alias,
			configkeys.KeyParams: cfg.Params,
		}
	}
	return result
}

// LoadFromConfig creates storage instances from configuration
// Expected format: storage: { alias1: {class: ..., params: ...}, alias2: {...} }
func (sm *StorageManager) LoadFromConfig(cfg *config.Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	storageConfig := cfg.GetSubConfig(configkeys.SectionStorage)
	if storageConfig == nil {
		logger.Info("No storage configuration found; skipping storage initialization")
		return nil
	}

	// Get all keys under storage (each key is an alias)
	aliases := storageConfig.Keys()
	logger.Info("Storage aliases discovered from configuration", "count", len(aliases), "aliases", aliases)
	for _, alias := range aliases {
		instanceConfig := storageConfig.GetSubConfig(alias)
		if instanceConfig == nil {
			logger.Warn("Storage alias config missing; skipping", "alias", alias)
			continue
		}

		if err := sm.createStorageFromConfig(alias, instanceConfig, logger); err != nil {
			return fmt.Errorf("failed to create storage instance %s: %w", alias, err)
		}
	}

	return nil
}

// createStorageFromConfig creates a storage from a config sub-tree
func (sm *StorageManager) createStorageFromConfig(alias string, storageConfig *config.Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	className := storageConfig.GetString(configkeys.KeyClass)
	if className == "" {
		return fmt.Errorf("storage %s: class is required", alias)
	}

	// Extract parameters based on class
	paramsConfig := storageConfig.GetSubConfig(configkeys.KeyParams)
	var params []interface{}

	switch className {
	case configkeys.StorageClassStdFile:
		basePath := paramsConfig.GetString(configkeys.KeyBasePath)
		if basePath == "" {
			return fmt.Errorf("storage %s: basePath is required", alias)
		}
		params = []interface{}{basePath}

	case configkeys.StorageClassConcurrentFile:
		baseDir := paramsConfig.GetString(configkeys.KeyBaseDir)
		lockDir := paramsConfig.GetString(configkeys.KeyLockDir)
		lockTimeoutStr := paramsConfig.GetString(configkeys.KeyLockTimeout)
		if baseDir == "" || lockDir == "" || lockTimeoutStr == "" {
			return fmt.Errorf("storage %s: baseDir, lockDir, and lockTimeout are required", alias)
		}
		lockTimeout, err := time.ParseDuration(lockTimeoutStr)
		if err != nil {
			return fmt.Errorf("storage %s: invalid lockTimeout: %w", alias, err)
		}
		params = []interface{}{baseDir, lockDir, lockTimeout}

	case configkeys.StorageClassHashComputingFile:
		baseDir := paramsConfig.GetString(configkeys.KeyBaseDir)
		if baseDir == "" {
			return fmt.Errorf("storage %s: baseDir is required", alias)
		}
		lockDir := paramsConfig.GetString(configkeys.KeyLockDir)
		lockTimeoutStr := paramsConfig.GetString(configkeys.KeyLockTimeout)
		if lockDir != "" && lockTimeoutStr != "" {
			lockTimeout, err := time.ParseDuration(lockTimeoutStr)
			if err != nil {
				return fmt.Errorf("storage %s: invalid lockTimeout: %w", alias, err)
			}
			params = []interface{}{baseDir, lockDir, lockTimeout}
		} else {
			params = []interface{}{baseDir}
		}

	default:
		return fmt.Errorf("storage %s: unknown class %s", alias, className)
	}

	// Create storage instance
	_, err := sm.Create(className, alias, params...)
	if err != nil {
		return fmt.Errorf("failed to create storage %s: %w", alias, err)
	}

	logger.Info("Storage instance created", "alias", alias, "class", className, "params", sm.extractParams(className, params))
	return nil
}

// Get retrieves a storage instance by alias
func (sm *StorageManager) Get(alias string) (models.ArtifactStorage, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	storage, exists := sm.storages[alias]
	if !exists {
		return nil, fmt.Errorf("storage alias not found: %s", alias)
	}

	return storage, nil
}
