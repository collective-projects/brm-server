package configkeys

// YAML top-level sections
const (
	SectionStorage  = "storage"
	SectionRegistry = "registry"
)

// Common keys
const (
	KeyClass          = "class"
	KeyAlias          = "alias"
	KeyParams         = "params"
	KeyServiceBinding = "serviceBinding"
)

// Storage params keys
const (
	KeyBasePath    = "basePath"    // std.filestorage
	KeyBaseDir     = "baseDir"     // concurrent/hashcomputing
	KeyLockDir     = "lockDir"     // concurrent/hashcomputing
	KeyLockTimeout = "lockTimeout" // concurrent/hashcomputing
)

// Registry params keys
const (
	KeyStorageAlias = "storageAlias"
	KeyDescription  = "description"
	KeyUpstream     = "upstream"
	KeyCacheTTL     = "cacheTTL"
)

// Upstream keys
const (
	KeyURL      = "url"
	KeyUsername = "username"
	KeyPassword = "password"
	KeyTTL      = "ttl"
)

// Service binding keys
const (
	KeyIP   = "ip"
	KeyPort = "port"
)

// Storage class names
const (
	StorageClassStdFile           = "std.filestorage"
	StorageClassConcurrentFile    = "concurrent.filestorage"
	StorageClassHashComputingFile = "hashcomputing.filestorage"
)

// Registry class names
const (
	RegistryClassDockerProxy   = "docker.registry.proxy"
	RegistryClassDockerPrivate = "docker.registry.private"
)
