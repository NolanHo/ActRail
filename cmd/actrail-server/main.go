package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/httpapi"
	"actrail/internal/ws"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := ensureDataDir(cfg); err != nil {
		logger.Error("actrail data dir init failed", "dir", cfg.Storage.DataDir, "err", err)
		os.Exit(1)
	}

	service, err := app.NewStub(cfg)
	if err != nil {
		logger.Error("actrail service init failed", "sqlite_path", cfg.SQLitePath(), "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := service.Close(); err != nil {
			logger.Error("actrail service close failed", "err", err)
		}
	}()
	replay, err := ws.NewReplayBuffer(cfg.Protocol.ResumeBuffer)
	if err != nil {
		logger.Error("invalid websocket replay buffer", "err", err)
		os.Exit(1)
	}
	registry := ws.NewRegistry()
	publisher := ws.NewPublisher(registry, replay)
	bridge := ws.NewAppBridge(service, service, publisher)
	service.SetRuntimeEventSink(bridge)
	handler := httpapi.New(cfg, service, ws.NewHandler(cfg,
		ws.WithRegistry(registry),
		ws.WithReplayBuffer(replay),
		ws.WithCommandTarget(bridge),
	))
	server := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("actrail server starting", "addr", cfg.Addr(), "protocol_version", cfg.Protocol.Version, "data_dir", cfg.Storage.DataDir, "sqlite_path", cfg.SQLitePath())
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("actrail server shutdown failed", "err", err)
			os.Exit(1)
		}
		logger.Info("actrail server stopped")
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("actrail server crashed", "err", err)
			os.Exit(1)
		}
	}
}

func ensureDataDir(cfg config.Config) error {
	return cfg.Storage.EnsureDir()
}
