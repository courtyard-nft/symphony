// Package main is the entrypoint for the Symphony service.
// It parses CLI flags, loads the workflow, initializes all components,
// and starts the orchestration loop with optional HTTP server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/fsnotify/fsnotify"

	"github.com/courtyard-nft/symphony/internal/config"
	"github.com/courtyard-nft/symphony/internal/linear"
	"github.com/courtyard-nft/symphony/internal/orchestrator"
	"github.com/courtyard-nft/symphony/internal/server"
	"github.com/courtyard-nft/symphony/internal/workflow"
	"github.com/courtyard-nft/symphony/internal/workspace"
)

func main() {
	// CLI flags
	workflowPath := flag.String("workflow", "", "Path to WORKFLOW.md (default: ./WORKFLOW.md)")
	port := flag.Int("port", 0, "HTTP server port (overrides server.port in WORKFLOW.md)")
	flag.Parse()

	// If positional arg is provided, use it as workflow path
	if flag.NArg() > 0 && *workflowPath == "" {
		*workflowPath = flag.Arg(0)
	}

	// Default workflow path
	if *workflowPath == "" {
		*workflowPath = "WORKFLOW.md"
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(*workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving workflow path: %v\n", err)
		os.Exit(1)
	}

	// Set up structured logging
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	logger.Info("symphony starting", "workflow", absPath)

	// Load workflow
	loader := workflow.NewLoader(absPath)
	def, err := loader.Load()
	if err != nil {
		logger.Error("failed to load workflow", "error", err)
		os.Exit(1)
	}

	// Initialize config
	cfg := config.New(def.Config)

	// Validate config at startup
	if err := cfg.ValidateDispatch(); err != nil {
		logger.Error("startup validation failed", "error", err)
		os.Exit(1)
	}

	// Initialize components
	tracker := linear.NewClient(cfg.TrackerEndpoint(), cfg.TrackerAPIKey(), logger)
	wsMgr := workspace.NewManager(cfg.WorkspaceRoot(), logger)
	orch := orchestrator.NewOrchestrator(cfg, loader, tracker, wsMgr, logger)

	// Determine HTTP server port
	serverPort := cfg.ServerPort()
	if *port > 0 {
		serverPort = *port // CLI --port overrides config
	}

	// Start HTTP server if port is configured
	var httpServer *server.Server
	if serverPort > 0 {
		httpServer = server.NewServer(orch, logger)
		if err := httpServer.Start(serverPort); err != nil {
			logger.Error("failed to start HTTP server", "error", err)
			os.Exit(1)
		}
		logger.Info("HTTP server listening", "addr", httpServer.Addr())
	}

	// Start file watcher for dynamic reload
	go watchWorkflow(absPath, loader, cfg, tracker, wsMgr, logger)

	// Start orchestrator
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := orch.Start(ctx); err != nil {
		logger.Error("orchestrator start failed", "error", err)
		os.Exit(1)
	}

	logger.Info("symphony running")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	logger.Info("shutdown signal received", "signal", sig)

	// Graceful shutdown
	orch.Stop()
	if httpServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10000000000) // 10s
		defer shutdownCancel()
		if err := httpServer.Stop(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown error", "error", err)
		}
	}

	logger.Info("symphony stopped")
}

// watchWorkflow sets up fsnotify file watching for dynamic reload.
func watchWorkflow(path string, loader *workflow.Loader, cfg *config.Config, tracker *linear.Client, wsMgr *workspace.Manager, logger *slog.Logger) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error("failed to create file watcher", "error", err)
		return
	}
	defer watcher.Close()

	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		logger.Error("failed to watch workflow directory", "error", err, "dir", dir)
		return
	}

	logger.Info("watching for workflow changes", "path", path)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Check if this is our file
			absEvent, _ := filepath.Abs(event.Name)
			absPath, _ := filepath.Abs(path)
			if absEvent != absPath {
				continue
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				logger.Info("workflow file changed, reloading", "path", path)
				def, err := loader.Load()
				if err != nil {
					logger.Error("workflow reload failed, keeping last good config", "error", err)
					loader.SetError(err)
					continue
				}
				// Re-apply config
				cfg.Update(def.Config)
				// Update dependent components
				tracker.UpdateConfig(cfg.TrackerEndpoint(), cfg.TrackerAPIKey())
				wsMgr.UpdateRoot(cfg.WorkspaceRoot())
				logger.Info("workflow reloaded successfully")
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Error("file watcher error", "error", err)
		}
	}
}
