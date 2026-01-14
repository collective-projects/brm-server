package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/collective-projects/brm-config/pkg/config"
	"github.com/collective-projects/brm-server/internal/registry"
	"github.com/collective-projects/brm-server/internal/storage"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger with configured log level
	logLevel := cfg.GetLogLevel(slog.LevelInfo)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	logger.Info("Initializing BRM Server")

	// Initialize storage
	if err := initializeStorage(cfg, logger); err != nil {
		logger.Error("Failed to initialize storage", "error", err)
		os.Exit(1)
	}

	// Initialize registries
	if err := initializeRegistry(cfg, logger); err != nil {
		logger.Error("Failed to initialize registries", "error", err)
		os.Exit(1)
	}

	// Get RegistryManager and start all servers
	registryManager := registry.GetManager()

	// Get server timeout configuration (with defaults)
	serverCfg := cfg.GetSubConfig("server")
	readTimeout := time.Duration(serverCfg.GetIntWithDefault("readTimeout", 15)) * time.Second
	writeTimeout := time.Duration(serverCfg.GetIntWithDefault("writeTimeout", 15)) * time.Second
	idleTimeout := time.Duration(serverCfg.GetIntWithDefault("idleTimeout", 60)) * time.Second

	// Start all registry HTTP servers
	servers, err := registryManager.StartAllServers(readTimeout, writeTimeout, idleTimeout, logger)
	if err != nil {
		logger.Error("Failed to start registry servers", "error", err)
		os.Exit(1)
	}

	if len(servers) == 0 {
		logger.Warn("No registries with service bindings configured")
		os.Exit(0)
	}

	logger.Info("BRM Server started successfully", "registryCount", len(servers))
	logger.Info("Press Ctrl+C to shutdown gracefully")

	// Setup graceful shutdown
	setupGracefulShutdown(servers, logger)
}

// initializeStorage loads storage configuration and creates storage instances
func initializeStorage(cfg *config.Config, logger *slog.Logger) error {
	storageManager := storage.GetManager()

	// Load storage instances from configuration
	if err := storageManager.LoadFromConfig(cfg); err != nil {
		return fmt.Errorf("failed to load storage configuration: %w", err)
	}

	logger.Info("Storage instances initialized successfully")
	return nil
}

// initializeRegistry loads registry configuration and creates registry instances
func initializeRegistry(cfg *config.Config, logger *slog.Logger) error {
	registryManager := registry.GetManager()

	// Load registry instances from configuration
	if err := registryManager.LoadFromConfig(cfg); err != nil {
		return fmt.Errorf("failed to load registry configuration: %w", err)
	}

	logger.Info("Registry instances initialized successfully")
	return nil
}

// setupGracefulShutdown handles OS signals and gracefully shuts down all HTTP servers
func setupGracefulShutdown(servers []*registry.ServerInfo, logger *slog.Logger) {
	// Wait for interrupt signal (Ctrl+C)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal
	<-sigChan

	logger.Info("Shutdown signal received, shutting down servers...")

	// Create shutdown context with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Shutdown all servers in parallel
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Add(1)
		go func(server *registry.ServerInfo) {
			defer wg.Done()
			logger.Info("Shutting down registry server", "alias", server.RegistryAlias, "address", server.Address)
			if err := server.Server.Shutdown(shutdownCtx); err != nil {
				logger.Error("Error shutting down server", "alias", server.RegistryAlias, "address", server.Address, "error", err)
			} else {
				logger.Info("Registry server shut down successfully", "alias", server.RegistryAlias, "address", server.Address)
			}
		}(srv)
	}

	// Wait for all servers to shutdown
	wg.Wait()

	logger.Info("All servers shut down successfully")
}
